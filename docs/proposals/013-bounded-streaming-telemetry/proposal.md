# Bounded-Memory Streaming Telemetry

## Status

Proposed. This document requests agreement on the recorder contract, resource
limits, and rollout before changing production telemetry behavior.

## Problem

For sampled inference requests, `internal/tracing/span.go` retains every typed
response chunk until `EndSpan`. This happens before the recorder decides whether
message content should be emitted. Disabling GenAI message-content capture
therefore does not disable retention of response content.

Anthropic's GenAI recorder also folds the entire stream before extracting usage,
even when message-content capture is disabled. The fold repeatedly appends to
immutable strings. Long responses can incur substantially more cumulative
allocation than their payload size, in addition to retaining the source chunks.
These costs are multiplied by the number of concurrent sampled requests.

This is a telemetry resource-management problem. It does not establish an OOM
threshold or imply that unsampled requests retain chunks through this path.

## Goals

- Extract response identity, usage, and finish reasons incrementally.
- Bound retained telemetry state independently of the number of stream events.
- Bound optional captured output and mark incomplete content explicitly.
- Continue extracting final usage after content capture reaches its limit.
- Preserve downstream response bytes and the existing provider-specific meaning
  of cumulative versus incremental usage fields.
- Support GenAI and OpenInference without changing their content opt-in rules.

## Non-Goals

This does not change Envoy body buffering, request admission, quota enforcement,
upstream retries, or client-visible response truncation. It is separate from
[full-duplex ext_proc support](https://github.com/theagentrouter/agent-router/issues/527).
It does not promise exact token totals if the upstream never supplies them.
Request and unary-response capture limits are follow-up work.

## Baseline Measurement

`BenchmarkMessageSpanStream` uses the real Anthropic GenAI recorder and a sampled
SDK span, with content capture enabled and disabled. It creates independently
owned 128-byte text deltas, follows them with usage, and ends the span so folding
and attribute construction are included. No exporter is configured.

```sh
go test ./internal/tracing -run '^$' -bench '^BenchmarkMessageSpanStream$' -benchtime=1x -count=3 -benchmem
```

The workloads contain 128, 1,024, and 8,192 deltas (16 KiB, 128 KiB, and 1 MiB
of text). `B/op` measures cumulative allocation for the entire stream, including
synthetic event creation; it is not retained heap or peak RSS. Compare like-for-
like runs on the same toolchain. Heap profiles and concurrent-stream benchmarks
are required before choosing defaults or making capacity claims.

### Initial Results

Measured on Windows/amd64, Intel i7-11800H, Go 1.26.6, commit `1899d8d4`, with
the benchmarks in this proposal applied. This is the parent of the Go 1.27.1
toolchain upgrade; `internal/tracing` and the Anthropic folding implementation
are unchanged between that commit and proposal base `4b317d8a`. These are not
measurements of the new toolchain. Local Go 1.27.1 download attempts failed.

| Text payload | Chunks | Full span, capture off, B/op | Full span, capture on, B/op | Fold only, B/op |
| ------------ | ------ | ---------------------------- | --------------------------- | --------------- |
| 16 KiB       | 128    | 1,150,600                    | 1,354,352                   | 1,108,320       |
| 128 KiB      | 1,024  | 70,826,704                   | 71,387,616                  | 70,479,280      |
| 1 MiB        | 8,192  | 4,330,508,344                | 4,332,768,960               | 4,327,705,008   |

The table uses the second of two single-iteration runs to reduce cold-start
effects; it is an illustrative baseline, not a statistically rigorous latency
comparison. The separate `BenchmarkMessagesResponseFromStream` excludes chunk
creation from its timed loop and isolates the same folding code. Its allocation
growth explains most of the end-to-end cost: repeatedly appending fixed-size
deltas to a string copies the growing prefix, yielding quadratic copied bytes.
About 4.03 GiB allocated for a 1 MiB response is not a claim of 4.03 GiB resident
memory. Garbage collection can reclaim intermediate strings during the fold.

```sh
go test ./internal/apischema/anthropic -run '^$' -bench '^BenchmarkMessagesResponseFromStream$' -benchtime=1x -count=2 -benchmem
```

## Proposed Recorder Contract

Add an optional streaming-recorder factory beside the existing response-recorder
interface. It constructs a request-local accumulator, consumes one typed chunk
at a time, and finalizes attributes when the span ends. A recorder implementing
this interface bypasses `span.chunks`; unmigrated endpoints use the existing path.
An accumulator must copy only the fields it needs and must never retain a whole
chunk or modify the caller's data.

Keep transport parsing and client delivery unchanged. A telemetry capture limit
must never return an error to the data plane or terminate an upstream stream.

### Metadata

Maintain the last reported response identity, provider-defined usage fields, and
bounded per-choice or per-block finish state. Do not sum cumulative token counts.
Limit identity string lengths and the number of tracked choices/blocks so that
an increasing number of indices cannot bypass the memory budget. Overflow marks
telemetry incomplete; it must not fabricate zero usage or a successful finish.

On cancellation or failure, finalize whatever metadata has been observed, record
the error, and release the accumulator. Successful completion, cancellation, and
error paths must each finalize exactly once. Captured content must not be
duplicated across error and completion paths.

### Optional Content

Use a separate bounded content collector. A shared byte budget covers all text,
thinking, and tool-argument fragments in a response, not one budget per block.
Also bound block counts, identifiers, and collector overhead. Enforce budgets
before copying or appending, not by truncating a fully assembled response later.

When the byte budget is reached, stop collecting content but keep consuming
metadata. Preserve UTF-8 boundaries. Emit an explicit truncation/incomplete
indicator; never present partial tool-argument JSON as a complete parsed object.
Whether to retain a labeled raw prefix or omit a truncated tool argument is an
open compatibility decision for each convention.

Proposed configuration concepts are a response-content byte budget and a maximum
number of tracked blocks/choices. Exact names and defaults require agreement.
They are gateway settings, not new OpenTelemetry standard environment variables.
Content remains disabled when the selected convention's current privacy settings
disable it. A limit does not enable capture.

### Memory Contract

For fixed configured limits, retained accumulator state should be bounded by the
content budget plus bounded metadata and block state, rather than total response
length. Total allocations can still grow as events are decoded; constant `B/op`
is not a valid acceptance criterion. Exporter queues and already-materialized
provider chunks are outside this accumulator's guarantee.

## Rollout

1. Merge the reproducible baseline and agree on resource and compatibility rules.
2. Implement the accumulator interface and the GenAI Anthropic metadata-only
   path. Verify attributes against existing fixtures, including cache usage.
3. Add budgeted content capture and explicit truncation semantics. Establish a
   default from heap and concurrency measurements, with release notes for any
   newly limited captured content.
4. Migrate Chat Completions, Responses, and OpenInference incrementally. Keep an
   explicit endpoint coverage matrix; do not claim all streaming paths are
   bounded while fallback recorders still retain chunks.

## Validation

- Compare old and new metadata on existing provider fixtures, including usage
  arriving only at the end, empty streams, interleaved tool blocks, and errors.
- Verify caller chunks remain unchanged and independent accumulators do not
  share mutable state.
- Exercise budget boundaries with multibyte text and partial tool arguments.
- Feed many deltas with capture disabled or saturated and measure retained heap.
- Test excessive block/choice counts and long identifiers independently of text.
- Verify final usage survives capture overflow and cancellation does not leak
  the accumulator or produce a second span end.
- Replay streams through the data plane and assert byte-for-byte client output
  equality; profile concurrent sampled streams separately from microbenchmarks.

## Alternatives

SDK attribute-length limits act after chunks have been retained and often after
the response is assembled, so they do not solve this problem. Dropping all chunks
after a fixed event count loses final usage. A larger process memory limit merely
moves the capacity threshold. Lower sampling can reduce exposure but does not
bound the cost of an individual sampled stream.

## References

- [Current chunk retention](../../../internal/tracing/span.go)
- [GenAI recorders](../../../internal/tracing/otelgenai/recorders.go)
- [Anthropic folding](../../../internal/apischema/anthropic/stream_fold.go)
- [GenAI content capture guidance](https://github.com/open-telemetry/semantic-conventions-genai/blob/main/docs/gen-ai/gen-ai-spans.md)
