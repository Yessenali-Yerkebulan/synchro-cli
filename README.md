# synchro

An AI team in your terminal.

Give it a goal. A product manager plans it, a researcher grounds it in real
sources, a developer writes the code and commits it to git, and a reviewer finds
the problems before you do.

No account. No subscription. No server. Everything lives in `~/.synchro` as
plain JSON, and a local [Ollama](https://ollama.com) install is enough to run
all of it — free, unlimited, and offline.

```
$ synchro

  * SYNCHRO  v0.1.0 - free, local, open source
  No account, no subscription, no server.

  default > MVP Builder > Product Lead (PRODUCT_MANAGER, ollama/qwen3)
  type a task, or /help for commands

> design a habit tracker and build it
```

## Install

Requires Go 1.22 or newer. There are no other dependencies, and no database,
container or service to run.

```sh
go install github.com/synchro/synchro-cli@latest
```

Or build from a clone:

```sh
git clone https://github.com/synchro/synchro-cli
cd synchro-cli
go build -o synchro .
```

Then set up a model:

```sh
synchro init        # finds Ollama, offers a free cloud key, creates a team
synchro doctor      # checks what is and isn't ready
```

## Getting a model

**Ollama (recommended).** Runs on your machine, needs no key, no card, and
works with the network off.

```sh
ollama pull qwen3          # or llama3.2, gemma3, mistral
synchro init
```

**A free cloud key**, if you want faster and stronger models. All three of
these have a free tier and none ask for a card:

| Provider | Free how | Key |
| --- | --- | --- |
| Gemini | Generative Language API free tier | https://aistudio.google.com/apikey |
| OpenRouter | any model whose id ends in `:free` | https://openrouter.ai/keys |
| Groq | free tier, very fast | https://console.groq.com/keys |

```sh
synchro keys set gemini
synchro models            # see which models cost nothing
```

OpenAI, Anthropic and DeepSeek are supported too. Nothing is gated: a paid
provider is just your own key and your own bill, and synchro tells you when a
model is not free rather than quietly spending your money.

## The model

```
workspace
└── team          a set of agents with a role each
    └── agent     researcher / product manager / developer / QA / critic / ...
        └── project    where generated code accumulates, committed to git
            └── task   one piece of work
```

A team also owns **workflows**: a graph of agents, with dependencies between
them, retry policies and per-node backoff.

Starter templates, from `synchro team templates`:

- `mvp` — research, spec, build, review
- `development` — spec a feature, build it, test it
- `research-product` — research a market, recommend, stress-test
- `marketing` — research, plan a campaign, critique it
- `solo` — one all-round agent

## The shell

Run `synchro` with no arguments to get the interactive prompt. Anything that
isn't a slash command is treated as a task for the active agent.

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

/provider <name> [model] switch the active agent's model
/models [provider]       see which models are free
/keys                    see which providers are ready
/config                  show settings
/status                  current context and spend estimate
/doctor                  check the setup
/history                 list past tasks
/reset                   clear the remembered context
/exit                    quit
```

The prompt always shows where you are, and remembers it between runs. `Ctrl+C`
cancels a run in progress; a second one at the prompt quits.

## Pipelines

A pipeline runs a chain of roles, feeding each one's output into the next.

```sh
synchro pipeline --list
synchro pipeline "a habit tracker with streaks and a weekly view"
synchro pipeline --mode review "add password reset to the API"
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

## Workflows

Where a pipeline is a straight line, a workflow is a graph. Nodes declare what
they depend on, and the runner works out the order, retries failures with
exponential backoff, and keeps the output of each node for the next.

```sh
synchro wf new "ship it" --agents researcher,developer,qa
synchro wf graph "ship it"
synchro wf run "ship it" "add dark mode"
```

## Generated code

A DEVELOPER agent is asked to emit named files rather than one wall of text:

````
FILE: main.go
```go
package main
```
````

Those files are written into `~/.synchro/repos/<project>/` and committed with
git, so what an agent built is real, reviewable history. Paths that try to
escape the project directory are rejected, and the commit is attributed to
`synchro` rather than to you.

## Where things live

`~/.synchro`, or `$SYNCHRO_HOME`, or `--home`:

```
config.json         settings
state.json          workspaces, teams, agents, projects, tasks, workflows
credentials.json    API keys, mode 0600
repos/<project>/    generated code, one git repo per project
history             shell input history
```

All plain JSON. Read it, script it, delete it, move it to another machine.

## Everything is optional

- `git` is only needed to commit generated code.
- `OLLAMA` is optional if you have a cloud key.
- A project is optional unless you want code committed.
- Researcher web search needs a `TAVILY_API_KEY`, and degrades to no search
  without one.

Run `synchro doctor` at any time to see what is ready and what is not.

## Scripts

`--json` makes output machine-readable where a command supports it:

```sh
synchro run --json "write a README for this repo" | jq -r .output
```

## Development

```sh
go test ./...
go build -o synchro .
```

## License

MIT. See [LICENSE](LICENSE).
