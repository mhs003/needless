# Writing commands

A command is one `.nsc` file. This guide is about writing them well; the
authoritative language definition lives with the project specification, not
here.

## Where commands live

```
~/.needless/commands/
├── default.nsc            ->  default
├── git/status.nsc         ->  git/status
└── project/start.nsc      ->  project/start
```

The path below the command directory, without the extension, is the ID. That is
the name you pass to `n --edit-command` and the name the model chooses between.
Subdirectories are only there to organise things; nothing else depends on them.

If `command_roots` is configured, the first root that has an ID wins, so your own
directory can shadow a shared one.

## The four sections

```nsc
instruction """
Start the development server for a project.
"""

confirm false

args {
    project: string
    port: int = 8000
}

run {
    env {
        PROJECT = project
        PORT = port
    }

    print("Starting " + project + " on port " + string(port))

    exec bash <<(
set -e
cd "$PROJECT"
)<<
}
```

`instruction` and `run` are required; `args` and `confirm` are optional.
`instruction` must be non-empty — a command with no description can never be
matched, so it is rejected when the file is read.

## Writing an `instruction` that matches

The instruction is the *only* thing the model sees when deciding which command a
prompt refers to. It is not a prompt to the model, and nothing in it is a
template: `{{anything}}` is literal text.

- **Describe what the command does, in the words a user would use to ask for
  it.** "Show the current Git status of a project" beats "Invoke git status and
  print the result".
- **Say when to use it, if that is not obvious.** A second sentence like "Use
  this when the user asks what has changed or what is modified" helps.
- **Keep instructions distinguishable.** Two commands whose instructions both
  read "Manage a project's files" will be picked between unpredictably. If two
  commands overlap, say in each instruction how they differ.
- **Leave the implementation out of it.** "Run tar with gzip into /tmp" gives the
  model nothing to match and hides what the command is for.

## Arguments

`args` declares the slots the model may fill from the prompt. The types are
`string`, `int`, `float` and `bool`.

```nsc
args {
    project: string
    port: int = 8000
    detached: bool = true
    ratio: float = 0.5
}
```

**An argument with no default is required.** If the prompt does not supply a
value, the command does not run: `n` says which value it could not find and, if
you have configured one, hands over to the fallback. This is deliberate — running
the script with a hole in it would fail somewhere deep inside your shell script
instead.

**A meaningless value counts as "no value".** A model with nothing to put in a
slot still has to return *something*, and what it returns is `""`, `null`, or a
bare `false`. None of those is taken at face value: the argument counts as
unsupplied, so it takes its default or, if it has none, stops the command. If you
genuinely want an empty value to be allowed, write `note: string = ""` and it
will be used. A number is different — `3000` written for a string slot really
does become `"3000"`, because that is a conversion the model may have meant.

Names are lower-case words, and they are only identifiers — `project`, not
`--project`.

## Getting values into a script

`exec` hands the body to another interpreter **byte for byte**. nscript does not
interpolate, substitute, trim or re-indent anything inside `<<( )<<`, which is
what makes it safe to embed PHP, Python or a heredoc-containing shell script.

Values therefore travel through the environment:

```nsc
instruction """
Serve a project with the PHP development server.
"""

args {
    project: string
    port: int = 8000
}

run {
    env {
        PROJECT = project
        PORT = port
    }

    exec bash <<(
set -e
cd "$PROJECT"
php artisan serve --port "$PORT"
)<<
}
```

Quoting is then the shell's business, and a `project` containing spaces or quotes
cannot break out into the script. Adding a value to the command line instead
would reintroduce exactly the quoting problem the language is avoiding.

The interpreter may be a name on `PATH` (`bash`) or a path (`/usr/bin/php`).

## Confirmation

```nsc
confirm true
```

With `confirm true`, `n` prints the command and asks before running anything;
anything other than `y`/`yes` — including no answer, or no terminal at all —
declines and exits 1. The arguments are not shown, so a password declared as an
argument is not echoed.

**The default is `false`.** A command that omits `confirm` runs without asking,
so if it destroys something, say so explicitly. Put this next to the `run` block
you are protecting, not in a comment.

## Native constructs

Small things belong in nscript rather than in a shell:

```nsc
instruction """
Report which shell a project is configured to use.
"""

confirm false

run {
    fn describe(label: string, value: string) {
        print(label + ": " + value)
    }

    let shell = "bash"

    describe("shell", shell)

    if shell == "bash" {
        print("Using a POSIX shell.")
    } else {
        print("Using " + shell + ".")
    }

    if shell == "" {
        error("no shell was configured")
        return
    }
}
```

`print` writes to stdout, which is the command's output. `error` stops the
command with a message and exit code 1, and anything printed before it is kept.
`return` ends the run cleanly. Both are different from a shell script exiting
non-zero, which also stops the command but reports the child's status.

Keep it small: nscript is for choosing and passing values, and `exec` is the
escape hatch for everything else.

## Three worked examples

### A read-only report

```nsc
instruction """
List the largest files in a directory.

Use this command when the user asks what is taking up space.
"""

confirm false

args {
    path: string
    count: int = 10
}

run {
    env {
        PATH_ARG = path
        COUNT = count
    }

    exec bash <<(
set -e
if [ ! -d "$PATH_ARG" ]; then
    echo "no such directory: $PATH_ARG" >&2
    exit 1
fi
du -h --max-depth=1 "$PATH_ARG" 2>/dev/null | sort -hr | head -n "$COUNT"
)<<
}
```

### A destructive command, guarded

```nsc
instruction """
Delete a project's build output.

Use this command when the user asks to clean, clear, or remove generated
build files.
"""

confirm true

args {
    project: string
    dry_run: bool = false
}

run {
    env {
        PROJECT = project
        DRY_RUN = dry_run
    }

    exec bash <<(
set -e
cd "$PROJECT"
for dir in build dist; do
    [ -e "$dir" ] || continue
    if [ "$DRY_RUN" = "true" ]; then
        echo "would remove $dir"
    else
        rm -rf "$dir"
        echo "removed $dir"
    fi
done
)<<
}
```

### One that needs nothing

```nsc
instruction """
Report the current date and time.

Use this command when the user asks what time or what day it is.
"""

confirm false

run {
    exec bash <<(
date '+%Y-%m-%d %H:%M %Z'
)<<
}
```

## Checking your work

```sh
n --list-commands        # a file that fails to parse is reported here
n --show-command git/status
n "show me the git status of /some/project"
```

Anything that does not parse is skipped with a warning naming the file and the
line, and everything else keeps working — so a typo in one command never takes
the others down.
