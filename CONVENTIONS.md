# Conventions

## Testing

- **Unit tests are for pure functions only.** If a test needs a fixture like `NewTestServices` or `RegisterFakeWorker`, it's not a unit test — delete it or move it to e2e. 
- **E2E is the real safety net.** Lifecycle, worker integration, persistence side effects, concurrency — all against a real worker. Fakes lie; e2e doesn't.
