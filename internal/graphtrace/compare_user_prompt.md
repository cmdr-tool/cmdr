## Previous trace

```json
{{.PreviousTraceJSON}}
```

## Current trace

```json
{{.CurrentTraceJSON}}
```

## Reminder

Your final assistant message MUST be a single valid JSON object matching the `ChangeSummary` schema (summary + changes). Empty `changes: []` is valid when nothing meaningful differs. No prose. No markdown fences.
