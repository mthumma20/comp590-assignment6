Bennett Rakower, Tracy Dang, Megha Thumma


# Reflection — Sleeping Barber in Elixir and Go

## 1. Process identity

In Elixir, process identity is built into the language through PIDs, so every process already has an address that can be passed in messages. That made customer-to-waiting-room and barber-to-customer replies feel very natural. In my Elixir version, I simply passed `self()` inside messages like `{:arrive, self(), id, arrival_ms}` so the waiting room could reply directly to that customer (Elixir: [elixir/sleeping_barber.exs L45-L49](elixir/sleeping_barber.exs#L45-L49)).

In Go, goroutines do not have any built-in identity, so I had to build that myself. I solved this by using each goroutine’s mailbox channel as its identity. Whenever a goroutine needed a reply, it created or used its own `chan Message` and passed that channel in the `From` field (Go: [go/main.go L62-L70](go/main.go#L62-L70)). For example, a customer sends `MsgArrive` with `From: mailbox`, and later the waiting room or barber can respond directly to that channel (Go: [go/main.go L106-L117](go/main.go#L106-L117)). In practice, this made channels act like Elixir PIDs.

## 2. State management

In Elixir, process state lives in recursive receive loops. That meant every time the waiting room or barber handled a message, it called itself again with updated state. For example, the waiting room carried queue, queue length, turnaway count, and barber sleeping state from one loop iteration to the next (Elixir: [elixir/sleeping_barber.exs L79-L140](elixir/sleeping_barber.exs#L79-L140)). This made the ownership of state very explicit, because the next state is always passed forward as function arguments.

In Go, state felt more like traditional local variables, because it lived inside the goroutine function. The barber kept fields like `haircutsCompleted`, `avgDurationMs`, and `avgRating` in a struct local to the barber goroutine (Go: [go/main.go L226-L239](go/main.go#L226-L239)). The waiting room likewise kept its queue and sleeping flag locally (Go: [go/main.go L155-L170](go/main.go#L155-L170)). Conceptually this was very similar to Elixir, because the state still belonged to exactly one concurrent entity, but syntactically it felt more familiar in Go. I thought Elixir felt especially natural for the waiting room loop, while Go felt a little more straightforward for the barber’s running averages.

## 3. The sleeping barber handshake

The sleeping and waking handshake was one of the most important parts of both implementations. In both languages, the barber begins by requesting the next customer (Elixir: [elixir/sleeping_barber.exs L149-L151](elixir/sleeping_barber.exs#L149-L151); Go: [go/main.go L241-L243](go/main.go#L241-L243)). If the waiting room has no one queued, it responds with a “none waiting” message (Elixir: [elixir/sleeping_barber.exs L120-L129](elixir/sleeping_barber.exs#L120-L129); Go: [go/main.go L193-L206](go/main.go#L193-L206)). At that point, the barber enters a sleeping state and waits for a wakeup message (Elixir: [elixir/sleeping_barber.exs L200-L221](elixir/sleeping_barber.exs#L200-L221); Go: [go/main.go L248-L281](go/main.go#L248-L281)). The waiting room maintains a boolean that tracks whether the barber is sleeping, so when a new customer arrives, it sends exactly one wakeup message and flips the flag back (Elixir: [elixir/sleeping_barber.exs L107-L114](elixir/sleeping_barber.exs#L107-L114); Go: [go/main.go L186-L189](go/main.go#L186-L189)).

In Elixir, mailbox behavior makes this somewhat forgiving because messages naturally queue up for the process. In Go, this handshake required more care because a missed wakeup could cause the barber to sleep forever. I used a buffered mailbox channel and an explicit sleeping loop so the barber could wait for `MsgWakeUp`, `MsgGetStats`, or `MsgShutdown` without losing messages (Go: [go/main.go L74-L76](go/main.go#L74-L76), [go/main.go L248-L281](go/main.go#L248-L281)). That made the Go version more manual, but also more explicit.

## 4. Message types

Elixir’s pattern matching made message definitions much lighter. I could use tuples like `{:customer_ready, customer_pid, customer_id}` or `{:rating, self(), id, rating}` and match directly on their shapes (Elixir: [elixir/sleeping_barber.exs L120-L124](elixir/sleeping_barber.exs#L120-L124), [elixir/sleeping_barber.exs L72-L75](elixir/sleeping_barber.exs#L72-L75)). That reduced boilerplate and made the code compact.

Go required much more structure. I defined a `MsgKind` enum and a `Message` struct with fields such as `Kind`, `From`, `CustomerID`, `Value`, and `Stats` (Go: [go/main.go L34-L70](go/main.go#L34-L70)). This added more boilerplate, but it also made the system’s message protocol very clear in one place. The practical effect was that Elixir felt faster to write, while Go felt more verbose but also more explicit and easier to trace when debugging.

## 5. `select` vs. `receive`

This difference mattered most in the barber process. Elixir’s `receive` reads from one mailbox and pattern matches over whatever message arrives, so I handled the barber’s behavior by switching between a normal loop and a sleeping loop (Elixir: [elixir/sleeping_barber.exs L154-L188](elixir/sleeping_barber.exs#L154-L188), [elixir/sleeping_barber.exs L200-L221](elixir/sleeping_barber.exs#L200-L221)). Since all messages go to the same mailbox, the code stayed centered around that one inbox.

In Go, `select` was useful because the barber sometimes needed to be ready for multiple possibilities, especially while sleeping. In my sleeping section, the barber could receive `MsgWakeUp`, `MsgGetStats`, `MsgShutdown`, or even a queued customer-related message (Go: [go/main.go L252-L280](go/main.go#L252-L280)). This made the sleeping state easier to express in Go than it would have been with plain channel reads alone. The waiting room used a simpler single-mailbox model, but the barber clearly benefited from `select`.

## 6. AI tool usage

I used AI tools mainly for the Elixir implementation, especially for syntax patterns around recursive receive loops, tuple message formats, and general process structure. The AI was helpful for generating a first draft of the customer, waiting room, and barber processes and for suggesting how to organize message passing cleanly.

What it got right was the general actor-style architecture: spawned processes, sending messages with process identities, and maintaining state inside process loops. What I had to fix was the sleeping/waking handshake, because that part is easy to get subtly wrong. I also had to clean up request/reply patterns to make sure the right PID was passed in the right messages and that stats and shutdown happened in the correct order. In other words, the AI was useful as a draft generator, but I still had to understand the protocol and revise the code so the behavior actually matched the assignment specification.
