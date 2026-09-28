<pre>
██████ ██  ██ ██  ██  █████ ██  ██ █████   ████
██     ██████ ███ ██ ██     ██  ██ ██  ██  ██  ██
█████   ████  ██████ ██     ██████ █████   ██  ██
    ██   ██   ██ ███ ██     ██  ██ ██ ██   ██  ██
██████   ██   ██  ██  █████ ██  ██ ██  ██   ████
 v0.1.0  ·  free, local, open source
</pre>

# synchro-cli

An AI team that works from your terminal.

You describe a goal. A product manager turns it into a spec, a researcher
grounds that spec in real sources, a developer writes the code and commits it
to git, and a QA agent attacks the result before you ever read it. The output of
each agent is the input of the next — no copy-pasting between prompts, no
context window you have to babysit.

It is a single Go binary. There is no account, no subscription, no server, and
no database. Everything it knows lives in `~/.synchro` as plain JSON that you can
read, diff, back up, script against, or delete.

```
$ synchro-cli

  ██████ ██  ██ ██  ██  █████ ██  ██ █████   ████
  ██     ██████ ███ ██ ██     ██  ██ ██  ██  ██  ██
  █████   ████  ██████ ██     ██████ █████   ██  ██
      ██   ██   ██ ███ ██     ██  ██ ██ ██   ██  ██
  ██████   ██   ██  ██  █████ ██  ██ ██  ██   ████
  v0.1.0  ·  free, local, open source

  No account, no subscription, no server. Your data lives in ~/.synchro

  default > MVP Builder > Product Lead (PRODUCT_MANAGER, ollama/qwen3)

> design a habit tracker with streaks and a weekly view
```

The logo is drawn with block characters and tinted top-to-bottom when your
terminal has colour on. It is skipped when output is piped into a file or
another program, and collapses to a single spaced line in terminals narrower
than 52 columns. Force or suppress it with `SYNCHRO_BANNER=always` or
`SYNCHRO_BANNER=never`, and reprint it inside the shell with `/banner`.

## Contents

- [Why](#why)
- [Install](#install)
- [Getting a model](#getting-a-model)
- [Quick start](#quick-start)
- [How the model fits together](#how-the-model-fits-together)
- [The interactive shell](#the-interactive-shell)
- [Pipelines](#pipelines)
- [Workflows](#workflows)
- [Generated code and git](#generated-code-and-git)
- [Command reference](#command-reference)
- [Providers](#providers)
- [Where your data lives](#where-your-data-lives)
- [What is optional](#what-is-optional)
- [Scripting](#scripting)
- [Development](#development)
- [License](#license)

## Why

A single chatbot gives you one opinion, in one pass, with no memory of what it
said three prompts ago. Real work is not like that. Somebody has to check the
facts. Somebody has to decide what to build. Somebody has to write it. Somebody
has to check the work.

Synchro is that sequence, made repeatable. The difference is not that the models
are smarter — it is that the roles are separated, each one is prompted for what
it is actually good at, and each one hands a concrete result to the next. A
developer agent is asked for named files rather than prose, so what it "wrote"
is real code in a real repository with a real commit.

## Install

Requires **Go 1.27.1 or newer**. There are no other dependencies — no runtime, no
container, no service to keep alive.

```sh
go install github.com/synchro/synchro-cli@latest
```

That puts the binary in `$(go env GOPATH)/bin`, so make sure that directory is on
your `PATH`:

```sh
export PATH="$PATH:$(go env GOPATH)/bin"   # add to ~/.bashrc or ~/.zshrc to keep it
```

Or from a clone:

```sh
git clone https://github.com/synchro/synchro-cli
cd synchro-cli
go build .
```

Check it:

```sh
synchro-cli version
synchro-cli doctor
```

If you built inside the clone and ran `synchro-cli` from that directory, prefix it
with `./` — a shell will not run a command out of the current directory on its
own:

```sh
./synchro-cli version
```

## Getting a model

Synchro talks to whatever model you point it at. Four of the options cost
nothing.

**Ollama — local, and the default.** Runs on your machine, needs no key, no
card, no account, and keeps working with the network unplugged. This is the
recommended setup.

```sh
ollama pull qwen3          # or llama3.2, gemma3, mistral
synchro-cli init
```

**Free cloud tiers**, if you want faster or stronger models. None of these ask
for a card:

| Provider | Why it is free | Where to get the key |
| --- | --- | --- |
| Gemini | Generative Language API free tier | https://aistudio.google.com/apikey |
| OpenRouter | any model whose id ends in `:free` | https://openrouter.ai/keys |
| Groq | free tier, very low latency | https://console.groq.com/keys |

```sh
synchro-cli keys set gemini
synchro-cli models              # which models cost nothing
```

OpenAI, Anthropic and DeepSeek are supported as well. Nothing is gated behind a
plan: a paid provider is simply your key and your bill. Synchro labels which
models are free rather than quietly spending your money.

## Quick start

```sh
# 1. detect a model, optionally store a free key, create a workspace and a team
synchro-cli init

# 2. give the team somewhere to put code
synchro-cli project new "habit tracker" --category saas

# 3. let the whole team loose on a goal
synchro-cli pipeline "a habit tracker with streaks and a weekly view"
```

That last command runs `RESEARCHER → PRODUCT_MANAGER → DEVELOPER → QA`. The
developer writes real files into a git repository and commits them; you get a
diff to read instead of a paragraph to skim.

`init` is not the only way to start. If you would rather work step by step, run
`synchro-cli` on its own for the interactive shell, or send one task to one agent:

```sh
synchro-cli run "summarise the tradeoffs of SQLite vs Postgres for this repo"
```

## How the model fits together

```
workspace
└── team           a group of agents, one role each
    └── agent      researcher / product manager / developer / QA / critic / ...
        └── project    where generated code accumulates, committed to git
            └── task       one unit of work and its result
```

A **role** is not a label. It decides behaviour:

| Role | What it does |
| --- | --- |
| `RESEARCHER` | grounds its answer in live web search, with sources |
| `PRODUCT_MANAGER` | turns a rough idea into a spec someone could build from |
| `DEVELOPER` | answers with real files, written to the project repo and committed |
| `QA` | reviews another agent's work for concrete bugs |
| `MARKETER` | plans campaigns and positioning |
| `CRITIC` | attacks the weakest assumption in a draft |
| `CEO`, `SYNTHESIZER`, `VALIDATOR` | summarise, combine, and check output |

Teams can also own **workflows**: a graph of agents with dependencies between
them, retries, and per-node backoff.

Ready-made teams, from `synchro-cli team templates`:

| Template | Chain |
| --- | --- |
| `mvp` | PRODUCT_MANAGER → RESEARCHER → DEVELOPER → QA |
| `development` | PRODUCT_MANAGER → DEVELOPER → QA |
| `research-product` | RESEARCHER → PRODUCT_MANAGER → CRITIC |
| `marketing` | RESEARCHER → MARKETER → CRITIC |
| `solo` | DEVELOPER |

## The interactive shell

Run `synchro-cli` with no arguments. Anything that is not a slash command is treated
as a task for the active agent.

```
/help                    list commands
/agents, /agent <n>      list agents, or switch to one
/team, /team <n>         list teams, or switch to one
/ws, /ws <name>          list workspaces, or switch to one
/project, /project <n>   list projects, or switch to one
/new <name>              create a project and make it active

/tasks                   list tasks in the current project
/commit <id>             commit a task's generated files to git
/files                   list the code generated so far
/report                  write a Markdown delivery report

/pipeline <text>         run the whole team over a goal
/pipeline <mode> <text>  run a specific chain (idea, build, review, ...)
/mode <id>               set the default pipeline mode
/wf <name> <input>       run a saved multi-agent workflow
/wf                      list workflows

/provider <name> [model] switch the active agent's model
/models [provider]       see which models are free
/keys                    see which providers are ready
/config                  show settings
/status                  current context and spend estimate
/doctor                  check the setup
/context                 redraw the context line
/history                 list past tasks
/reset                   clear the remembered context
/clear                   clear the screen
/exit                    quit
```

The prompt always shows where you are and remembers it between runs, so `synchro-cli`
reopens in the same workspace, team, agent and project you left. `Ctrl+C` cancels
a run in progress; pressing it again at the prompt quits.

## Pipelines

A pipeline is a straight line: several agents in order, each one receiving the
previous one's output.

```sh
synchro-cli pipeline --list
synchro-cli pipeline --dry-run "add dark mode"    # see who would run, run nothing
synchro-cli pipeline "a habit tracker with streaks and a weekly view"
synchro-cli pipeline --mode review "add password reset to the API"
```

| Mode | Chain |
| --- | --- |
| `idea` | RESEARCHER → PRODUCT_MANAGER → DEVELOPER → QA |
| `spec` | PRODUCT_MANAGER |
| `build` | DEVELOPER |
| `fullstack` | DEVELOPER → DEVELOPER (backend, then frontend) |
| `review` | DEVELOPER → QA |
| `research` | RESEARCHER |
| `critique` | MARKETER → CRITIC |

`idea` is the default. The same modes work in the shell: `/pipeline review add
dark mode`.

## Workflows

Where a pipeline is a line, a workflow is a graph. Each node is an agent, each
edge says who runs next, and a node waits for all of its predecessors — so a graph
can genuinely branch and merge.

```sh
synchro-cli wf new "ship it" --agents researcher,developer,qa
synchro-cli wf graph "ship it"        # also available as: wf show
synchro-cli wf run "ship it" --input "add dark mode"
synchro-cli wf show "ship it"         # graph plus run history
```

Failed nodes are retried with exponential backoff, and each run is kept in
history so you can see what happened and when.

## Generated code and git

A DEVELOPER agent is asked to emit named files rather than one wall of text:

````
FILE: main.go
```go
package main
```
````

Those files are written into `~/.synchro/repos/<project>/` and committed to git,
so what an agent built is real, reviewable history rather than a claim about
what it built. The commit is attributed to `Synchro Developer Agent
<agents@synchro.local>`, not to you.

Paths that try to escape the project directory — absolute paths, `..`, drive
letters, UNC paths — are rejected before anything is written.

**Auto-commit is off by default.** You approve the code, then commit it
yourself:

```sh
synchro-cli files                # what was generated
synchro-cli commit <task-id>     # write it to git
```

To let developers commit on their own, enable it per workspace or globally:

```sh
synchro-cli ws new "my workspace" --auto-commit   # this workspace
synchro-cli config set auto_commit true           # every new workspace
```

## Command reference

Every command explains itself:

```sh
synchro-cli <command> --help
```

The ones you will use most:

| Command | What it does |
| --- | --- |
| `synchro-cli` | interactive shell |
| `synchro-cli init` | first-time setup: model, key, workspace, team |
| `synchro-cli run <text>` | give the active agent a task |
| `synchro-cli pipeline <text>` | run a chain of agents |
| `synchro-cli wf <subcommand>` | build and run workflow graphs |
| `synchro-cli task <subcommand>` | `new`, `run`, `show`, `rm` |
| `synchro-cli project <subcommand>` | `new`, `use`, `show`, `rm` |
| `synchro-cli agent <subcommand>` | `new`, `edit`, `use`, `show`, `rm` |
| `synchro-cli team <subcommand>` | `new`, `from`, `use`, `templates` |
| `synchro-cli ws <subcommand>` | `new`, `use`, `show`, `rm` |
| `synchro-cli commit <task-id>` | commit a task's generated files |
| `synchro-cli files` | list generated code for a project |
| `synchro-cli report [project]` | write a Markdown delivery report |
| `synchro-cli models [provider]` | which models you can use, and which are free |
| `synchro-cli keys` | manage provider API keys |
| `synchro-cli config` | read and change settings |
| `synchro-cli doctor` | check that everything needed is working |
| `synchro-cli version` | print the version |

Global flags work on every command: `-w/--workspace`, `-t/--team`,
`-a/--agent`, `-p/--project` to override the active context, `--json` for
machine-readable output, `--no-color`, `-y/--yes` to skip confirmations, and
`--home` to point at a different state directory.

Tasks can be addressed the way you see them: the row number from `synchro-cli task`,
the id, or the title.

```sh
synchro-cli task
synchro-cli task show 3
synchro-cli task run 3
```

## Providers

`ollama`, `gemini`, `openrouter`, `groq`, `openai`, `anthropic`, `deepseek`.

```sh
synchro-cli models                     # curated lists, free ones first
synchro-cli models ollama              # one provider
synchro-cli models --refresh           # ask providers for their live catalogue
synchro-cli models --all               # include paid models
```

Keys are stored in `credentials.json` with `0600` permissions, and an existing
environment variable of the same name (`GEMINI_API_KEY`, `OPENROUTER_API_KEY`,
`OPENAI_API_KEY`, …) is used instead if you prefer not to store it.

**Any OpenAI-compatible server works**, including a local one. Point the
provider at it and no code change is needed:

```sh
export SYNCHRO_OPENAI_URL=http://127.0.0.1:8080/v1
export OPENAI_API_KEY=dummy              # a local server usually ignores it
synchro-cli config set provider openai
```

The base URL is the only thing that uses the `SYNCHRO_` prefix, and it exists
for every provider: `SYNCHRO_OLLAMA_URL`, `SYNCHRO_GEMINI_URL`, and so on. The
Ollama URL is also `synchro-cli config set ollama_url ...`.

Settings you can change with `synchro-cli config set <key> <value>`:

`provider`, `model`, `ollama_url`, `temperature`, `max_tokens`, `stream`,
`search`, `auto_commit`, `color`, `request_timeout`.

## Where your data lives

`~/.synchro`, or `$SYNCHRO_HOME`, or `--home <dir>`:

```
config.json         settings
state.json          workspaces, teams, agents, projects, tasks, workflows
credentials.json    API keys, mode 0600
repos/<project>/    generated code, one git repository per project
history             shell input history
```

All plain JSON. Point `SYNCHRO_HOME` at a synced folder to move your whole setup
between machines, or delete the directory to start over.

## What is optional

Nothing below is required unless you want the feature it belongs to:

- `git` — only needed to commit generated code.
- `ollama` — optional if you have a cloud key.
- a project — optional unless you want code committed somewhere.
- web search — a `RESEARCHER` grounds itself in live results when
  `TAVILY_API_KEY` is set, and works without one, just with less grounding.
- auto-commit — off by default; generated code waits for you.

`synchro-cli doctor` tells you exactly what is ready and what is not, at any time.

## Scripting

`--json` makes output machine-readable wherever it is supported:

```sh
synchro-cli run --json "write a README for this repo" | jq -r .output
synchro-cli task --json | jq -r '.[] | [.id, .status, .title] | @tsv'
```

Because the state is plain JSON, you can also read it directly:

```sh
jq '.tasks | length' ~/.synchro/state.json
```

## Development

```sh
go test ./...                  # unit tests, no network needed
go build .
go vet ./...
```

Layout:

```
main.go
internal/cli/      cobra commands, REPL, output
internal/agents/   roles, FILE parsing, pipelines, workflow executor
internal/llm/      provider clients (openai, ollama, gemini, anthropic)
internal/store/    JSON persistence
internal/repo/     generated-file safety and git
internal/model/    domain types
internal/ui/       terminal rendering
```

Providers are the easiest thing to extend: add a `Style` in `internal/llm` and
an entry to `Registry`.

## License

MIT. See [LICENSE](LICENSE).
