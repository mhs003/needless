package needle

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"

	"github.com/mhs003/needless/needle/internal/worker"
)

// Config describes one Needle instance: the engine, the weights, and the
// static prefix (system prompt + tools) baked in at startup.
type Config struct {
	// EnginePath is the engine shared library. Empty means DefaultEnginePath().
	EnginePath string
	// WeightsPath is the .cact model archive. Required.
	WeightsPath string
	// SystemPrompt is optional environment context.
	SystemPrompt string
	// ToolsJSON is the JSON array of tool schemas. Empty means "[]".
	ToolsJSON string
	// ToolIndexPath is an optional saved retrieval index.
	ToolIndexPath string
	// BufferSize is the completion buffer in bytes. Zero means 64 KiB.
	BufferSize int
	// WorkerPath overrides discovery of the needle-worker binary. Tests and
	// unusual deployments use this; normally leave it empty.
	WorkerPath string
}

// Needle is a loaded model with a live conversation. It owns a worker
// subprocess; call Close when done.
type Needle struct {
	w        *worker.Worker
	prefix   int
	closeOne sync.Once
}

// New starts a worker, loads the weights, and builds the static prefix. It is
// slow (it maps the model), so do it once at startup and reuse the value.
func New(ctx context.Context, cfg Config) (*Needle, error) {
	if cfg.WeightsPath == "" {
		return nil, errors.New("needle: WeightsPath is required")
	}
	if _, err := os.Stat(cfg.WeightsPath); err != nil {
		return nil, fmt.Errorf("needle: weights: %w", err)
	}
	engine := cfg.EnginePath
	if engine == "" {
		var err error
		engine, err = DefaultEnginePath()
		if err != nil {
			return nil, err
		}
	}
	if _, err := os.Stat(engine); err != nil {
		return nil, fmt.Errorf("needle: engine: %w", err)
	}
	tools := cfg.ToolsJSON
	if tools == "" {
		tools = "[]"
	}
	bufSize := cfg.BufferSize
	if bufSize == 0 {
		bufSize = defaultBufferSize
	}

	w, err := worker.Start(ctx, cfg.WorkerPath, worker.Config{
		Library:    engine,
		Weights:    cfg.WeightsPath,
		System:     cfg.SystemPrompt,
		Tools:      tools,
		ToolIndex:  cfg.ToolIndexPath,
		BufferSize: bufSize,
	})
	if err != nil {
		return nil, err
	}
	return &Needle{w: w, prefix: w.PrefixTokens()}, nil
}

// Complete runs one turn and returns the model's raw JSON reply.
//
// The reply is a single JSON object containing "function_calls" (possibly
// empty), "reasoning" and "confidence". Parse it with the helpers in this
// package or json.Unmarshal into your own type.
func (n *Needle) Complete(ctx context.Context, input string, maxNewTokens int) (string, error) {
	if n == nil || n.w == nil {
		return "", errors.New("needle: nil receiver")
	}
	if maxNewTokens <= 0 {
		maxNewTokens = defaultMaxNewTokens
	}
	return n.w.Complete(ctx, input, maxNewTokens)
}

// Embed returns the embedding vector for text. Use it for local search and
// routing: cosine similarity over these vectors.
func (n *Needle) Embed(ctx context.Context, text string) ([]float32, error) {
	if n == nil || n.w == nil {
		return nil, errors.New("needle: nil receiver")
	}
	return n.w.Embed(ctx, text)
}

// EmbedDim returns the embedding dimension (cached after the first call).
func (n *Needle) EmbedDim(ctx context.Context) (int, error) {
	if n == nil || n.w == nil {
		return 0, errors.New("needle: nil receiver")
	}
	return n.w.EmbedDim(ctx)
}

// Reset clears the conversation history but keeps the model and prefix.
func (n *Needle) Reset(ctx context.Context) error {
	if n == nil || n.w == nil {
		return errors.New("needle: nil receiver")
	}
	return n.w.Reset(ctx)
}

// PrefixTokens reports the tokenised length of the static prefix. Useful for
// budgeting the context window.
func (n *Needle) PrefixTokens() int { return n.prefix }

// PID returns the worker process id (handy in tests and ops dashboards).
func (n *Needle) PID() int {
	if n == nil || n.w == nil {
		return -1
	}
	return n.w.PID()
}

// Close stops the worker. Safe to call more than once.
func (n *Needle) Close() error {
	if n == nil || n.w == nil {
		return nil
	}
	var err error
	n.closeOne.Do(func() { err = n.w.Close() })
	return err
}

const (
	defaultBufferSize   = 64 * 1024
	defaultMaxNewTokens = 512
	engineDir           = "engine"
)

// DefaultEnginePath looks for the engine shared library in this order:
//
//  1. $NEEDLE_ENGINE
//  2. <module root>/engine/<goos>-<goarch>/<libname>
//  3. the Python cache at ~/.cache/cactus-needle/v3/*/<libname>
//
// Setting NEEDLE_ENGINE is the escape hatch for deployments that ship the
// library themselves.
func DefaultEnginePath() (string, error) {
	if env := os.Getenv("NEEDLE_ENGINE"); env != "" {
		if _, err := os.Stat(env); err != nil {
			return "", fmt.Errorf("needle: NEEDLE_ENGINE=%q: %w", env, err)
		}
		return env, nil
	}
	for _, candidate := range candidateEnginePaths() {
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("needle: engine library not found for %s/%s; set NEEDLE_ENGINE or pass Config.EnginePath", runtime.GOOS, runtime.GOARCH)
}

func candidateEnginePaths() []string {
	lib := libraryName()
	platform := runtime.GOOS + "-" + runtime.GOARCH
	var out []string

	// Beside this source file's module: engine/<platform>/<lib>.
	if _, file, _, ok := runtime.Caller(0); ok {
		root := filepath.Dir(filepath.Dir(file)) // .../needle
		out = append(out, filepath.Join(root, engineDir, platform, lib))
	}
	// Python package cache.
	if home, err := os.UserHomeDir(); err == nil {
		parent := filepath.Join(home, ".cache", "cactus-needle", "v3")
		if entries, err := os.ReadDir(parent); err == nil {
			for _, e := range entries {
				if e.IsDir() {
					out = append(out, filepath.Join(parent, e.Name(), lib))
					out = append(out, filepath.Join(parent, e.Name(), "linux-x86_64", lib))
				}
			}
		}
	}
	return out
}

func libraryName() string {
	switch runtime.GOOS {
	case "darwin":
		return "libneedle.dylib"
	case "windows":
		return "needle.dll"
	default:
		return "libneedle.so"
	}
}
