// Package needle is a Go binding for the Cactus Compute Needle 3 on-device
// foundation model. It wraps the C ABI exposed by libneedle (needle.h) with a
// safe, idiomatic Go API.
//
// The Needle runtime is process-global and non-thread-safe, so this package
// spawns a dedicated worker subprocess (see internal/worker) that loads the
// model and the static prefix once, then serves complete/embed/reset requests
// over a framed JSON protocol on stdin/stdout. Multiple Needle values may
// coexist in a Go program — each gets its own worker and its own model.
//
// Typical usage:
//
//	w, err := needle.New(needle.Config{
//	    WeightsPath: "models/needle3.cact",
//	    ToolsJSON:   `[{"name":"get_weather", ...}]`,
//	})
//	if err != nil { return err }
//	defer w.Close()
//
//	out, err := w.Complete(ctx, "weather in Lagos?", 256)
//	emb, err  := w.Embed(ctx, "search query")
//
// See BINDINGS.md in this directory for ABI notes and protocol details.
package needle
