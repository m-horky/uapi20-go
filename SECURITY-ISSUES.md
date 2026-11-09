# Security Issues

- Model: DeepSeek 4.1 Flash
- Scope: file-descriptor call-option v2 API

## Trust boundaries

1. Peer process on an AF_UNIX socket (same or different UID) to the varlink service, including descriptors and peer credentials.
2. Service process to client process across SCM_RIGHTS: a received descriptor is a capability into the sender's namespace, so queueing, ownership, and duplication semantics are privilege-relevant.
3. Library API caller (service handler) to library: pushes caller-owned `*os.File` values that the library resolves to raw descriptors later.
4. Build environment and Go toolchain to shipped binary/library.
5. HTTP/WebSocket bridge client to bridge to local varlink service.

---

## Findings

### 1. Unbounded pre-dispatch descriptor buffering per connection (pre-existing; adjacent to the staged receive API)

- **Severity:** Medium, DREAD mean **4.8**
- **Files:** `v2/varlink/server_runtime.go:18-20` (`connQueueSize`), `:363-396` (`connReader`, buffered channel), `:128-129` (`fdBox` closes only after dispatch); `v2/varlink/fd_unix.go:17-19,61-64,89-116` (253 fds per message, queued per message); `v2/varlink/server.go:51-55` and `v2/varlink/listener.go:120-146` (`MaxConnections` zero means unlimited).
- **Attacker input and data flow:** A local peer sends call frames each carrying an SCM_RIGHTS control message with up to 253 descriptors. `fdConn.recv` -> `parseRights` -> `fdConn.queue` -> `takeMessageFDs` -> `connReader.run` (buffered channel of 16) -> `receivedMessage.fds` -> only then `handleMessage` creates `fdBox` and closes untaken descriptors. The reader goroutine runs ahead of the dispatcher, so one connection can hold roughly 17 x 253 = ~4300 descriptors in kernel/Go state while handlers are slow or while the peer pipelines calls to a blocking method. `MaxConnections` is unlimited by default, so this scales by connection count.
- **Preconditions:** Service sets `ServerConfig.AcceptFileDescriptors = true` (required only for FD-taking interfaces, but common for such a daemon), listening socket is reachable by the attacker (world-accessible `/run` path or TCP), and `MaxConnections` is left at zero (the default). No authentication is needed unless `Authorize` is configured; even then the descriptors are received and buffered before dispatch and before authorization.
- **Impact:** Descriptor-table exhaustion in the service process (`EMFILE`), failing `accept`, socket creation, and unrelated file operations: denial of service against a local or privileged daemon, potentially destabilizing other clients. Trust boundary: unprivileged peer to serving process.
- **Existing defenses:** `MSG_CTRUNC` handling caps one message at 253 descriptors and closes descriptors of truncated control data; `fdBox` closes descriptors the handler does not take; `SO_PASSRIGHTS=0` is set on non-accepting listeners on Linux >= 6.16. None of these bound the read-ahead queue or the aggregate across connections.
- **Remediation:** Bound descriptors per connection (for example, a semaphore acquired per received descriptor and released when the message is dispatched or dropped), reduce `connQueueSize` for FD-capable connections, or pause reading when queued descriptors exceed a budget. Document that FD-enabled services must set `MaxConnections` and an adequate `LimitNOFILE`, and that `MaxConnections` currently bounds goroutines/buffers but not descriptors.
- **DREAD:** Damage 5, Reproducibility 6, Exploitability 5, Affected users 4, Discoverability 4.

**Proposed inline comment (not applied), `server_runtime.go` near `newConnReader`:**
```go
// SECURITY: each message buffered here may own up to 253 descriptors
// (maxSCMFileDescriptors), and the channel holds connQueueSize messages.
// Nothing bounds the descriptors held before dispatch, and MaxConnections
// defaults to unlimited, so a peer on an FD-enabled service can exhaust the
// process descriptor table. Add an explicit descriptor budget here.
```

### 2. `ReceiveFiles` silently overwrites a previously stored descriptor set

- **Severity:** Medium, DREAD mean **4.2**
- **Files:** `v2/varlink/calloptions.go:34-46` (`ReceiveFiles` contract), `:76-84` (`deliver` assigns `*o.reply = files` unconditionally); `v2/varlink/client_runtime.go:343-372` (streaming `Recv`), `:485` (batch deliver); `docs/file-descriptors.md:69-79` (the staged example reuses one `files` variable for `Invoke` and then `stream.Recv`).
- **Attacker input and data flow:** A service attaches descriptors to a success reply (up to 253 per message via `PushFiles`). Client: `fdConn.recv` -> `parseRights` -> `fdConn.queue` -> `takeMessageFDs` -> `readReplyFDs` -> `deliver` -> `*o.reply = files`. If `*o.reply` already holds the previous reply's `Files`, that slice becomes unreachable without `Close`; only the `*os.File` finalizer eventually closes it. In a streaming loop using `stream.Recv(ctx, &out, ReceiveFiles(&files))`, every iteration replaces the previous set. The staged documentation example itself shows this reuse (`Invoke(... ReceiveFiles(&files))`, then `Recv(... ReceiveFiles(&files))`, with a single `defer files.Close()`, so the first set is never closed).
- **Preconditions:** Client enabled `ClientConfig.AcceptFileDescriptors`, calls a method that returns descriptors, and reuses the destination variable (the documented idiom). A malicious or buggy service controls how many descriptors arrive per reply and how many replies it sends.
- **Impact:** Client-side descriptor exhaustion and eventual `EMFILE` (self-denial of service; a long-running client stops being able to open files or sockets). No confidentiality boundary is crossed: the leaked descriptors are the client's own. GC finalizers mitigate but do not bound the window, especially in a goroutine blocked in `Recv`.
- **Existing defenses:** Error-reply and non-opted-in descriptors are closed by the library (this staging improves that); `AcceptFileDescriptors` is off by default; one reply is processed at a time, so the leak is bounded by stream length until GC runs.
- **Remediation:** Make `deliver` reject or drain a non-empty destination: return `ErrInvalidCallOption` when `*o.reply != nil`, or close the previous `Files` before overwriting. Document that `ReceiveFiles` stores a new set per successful call and that loop callers must close the previous set; fix the staged docs example to use a fresh variable or close before reuse.
- **DREAD:** Damage 3, Reproducibility 6, Exploitability 4, Affected users 4, Discoverability 4.

**Proposed inline comment (not applied), `calloptions.go` `deliver`:**
```go
// SECURITY: assigning over a non-nil destination drops the caller's
// previous Files without closing them. A streaming loop that reuses one
// destination leaks up to maxSCMFileDescriptors per reply until GC runs,
// which a service can turn into client descriptor exhaustion. Close the old
// value or reject a non-empty destination with ErrInvalidCallOption.
```

### 3. `Batch.Send` validates `ReceiveFiles`-on-oneway only after all requests are written

- **Severity:** Low, DREAD mean **3.8**
- **Files:** `v2/varlink/client_runtime.go:417-436` (`Batch.Invoke`/`Batch.Oneway` cannot fail at queue time), `:438-470` (`Send` writes every request, then rejects `op.opts.reply` for oneway ops), `client.go:57-62`.
- **Attacker input and data flow:** The local caller queues a oneway call with `ReceiveFiles`. `Send` serializes and writes every operation first (including that oneway call), then, in the reply loop, sees `op.oneway && op.opts.reply != nil`, returns `ErrInvalidCallOption`, and calls `b.client.fail`, closing the connection. The service has already received and may have executed the oneway call. A caller that treats the error as "nothing was sent" and retries after removing the option causes the side effect twice.
- **Preconditions:** Local API misuse only; no peer input beyond normal service execution of the already-written oneway. Impact requires a non-idempotent oneway method and a retry.
- **Impact:** Duplicate side effects and unexpected connection loss (integrity/availability of the client's interactions). Trust boundary: library API contract; the peer merely executes what was legitimately sent.
- **Existing defenses:** The unbatched `Oneway` and opening `Stream` reject `ReceiveFiles` before any write (`client_runtime.go:88-91,106-109`). The batch path lacks the equivalent pre-write check.
- **Remediation:** Validate all queued operations (method non-empty, option applicability, marshal/frame size) in a pre-write pass, and fail with `ErrInvalidCallOption` before any bytes are written; the staged docs already defer batch validation decisions to "tracked separately", so include this one in that work.
- **DREAD:** Damage 3, Reproducibility 6, Exploitability 4, Affected users 3, Discoverability 3.

### 4. `PushFiles` is not atomic on limit failure, leaving descriptors queued for a later reply

- **Severity:** Low, DREAD mean **3.4**
- **Files:** `v2/varlink/server_runtime.go:482-494` (`PushFiles` loops `PushFile` and returns on the first error without rolling back), `:496-502` (`takePushed`), `:528-562` (`Reply`/`ReplyError` drain whatever is queued).
- **Attacker input and data flow:** A service handler calls `PushFiles` with more files than the remaining 253-descriptor budget (for example 5 files when 250 are already queued, or 300 files at once). The prefix is appended to `c.pushed` with allocated indexes, then `PushFile` fails and `PushFiles` returns `(nil, err)`. The handler follows the error path and may close all files or return an error; a later `Reply`/`ReplyError` still drains the successfully queued prefix and attaches those descriptors to a reply whose parameters were not built around them, or writes stale descriptors that the handler already closed (`EBADF`, failed reply, connection teardown).
- **Preconditions:** Handler pushes near the 253-descriptor limit and relies on the documented "same semantics and failure modes as PushFile" contract, which implies all-or-nothing. The peer only triggers the handler.
- **Impact:** Protocol confusion (reply carries unindexed or unexpected descriptors), failed replies, connection teardown. Trust boundary: service descriptors to peer.
- **Existing defenses:** The per-message cap is enforced before append, nil files are rejected, and the queue is emptied per reply. There is no rollback or "discard queued descriptors" API.
- **Remediation:** Make `PushFiles` transactional: pre-check `len(c.pushed)+len(files) <= maxSCMFileDescriptors` and fail before appending anything, or return the indexes pushed so far and document that the caller must not treat a partial result as a success. Consider a `DiscardPushed()`/queue-reset helper for error paths.
- **DREAD:** Damage 3, Reproducibility 5, Exploitability 3, Affected users 3, Discoverability 3.

---

## Review notes (not confirmed as findings)

1. **`MSG_CTRUNC` with `accept=false` may fail the connection instead of silently discarding descriptors.** `fd_unix.go:89-115` treats `MSG_CTRUNC` as a malformed control message even when no control buffer was supplied (`oob = nil`), which contradicts `server.go:44-49` ("the descriptors are discarded when the connection is read"). Confirmation needs a kernel-version test of `recvmsg` with a zero-length control buffer on the non-`SO_PASSRIGHTS` path; the existing `TestFileDescriptorsNotAcceptedByDefault` tolerates both outcomes (`fd_test.go:143-173`). Impact would be limited to dropping the offending client's own connection.
2. **`PushFile` on a oneway call succeeds and returns an index that can never be delivered.** `server_runtime.go` sets `c.pushed = nil` for suppressed replies without closing anything; the docs (`docs/file-descriptors.md:127-129`) say copies stay valid, so there is no library-owned leak, but the returned index is misleading and the client-side API is stricter (`requireNoReply`). Consider rejecting `PushFile` when `IsOneway()`.
3. **`SendFiles` has no 253-descriptor pre-check.** `calloptions.go:18-32` passes any length to `WriteMsg`; `fd_unix.go:225-232` builds the cmsg and `sendmsg` fails (`EINVAL`/`EMSGSIZE`), after which `Invoke` calls `c.fail` and closes the connection (`client_runtime.go:47-53`). Local misuse only; symmetric validation with the server would give `ErrFileDescriptorsUnsupported` without a hard connection teardown.
4. **Registry discovery is first-writer-wins.** `registerInRegistry` creates `/run/varlink/registry` with `0755` and silently ignores `EEXIST` (`registry.go:36-47`), so a pre-existing or squatted symlink for an interface name is kept and may point at a different service. Exploitability depends entirely on deployment: the `/run` tree must be root-owned and not writable by the registering users. Worth a deployment note in `docs/`.
5. **Bridge hardening.** `httpbridge` sets only `ReadHeaderTimeout`; request bodies have a size limit but no read/idle timeout (`bridge.go:122-140`, `:326-348`). Transport errors are returned verbatim to HTTP clients (`writeCallError`, `bridge.go:375-388`), which can expose local socket paths; plain HTTP is served when `Config.TLS` is nil. Deployment-level items, unchanged by this staging.
6. **Tests ignore `PushFiles` errors** (`fd_test.go:82,323,337,641,708,754`), so finding 4's partial-push behavior is untested.
7. **Batch write-all-then-read remains a deadlock risk** with batches larger than the server read-ahead queue; already documented as a known issue in `DECISIONS.md` / prior reviews and unchanged here.

## Security strengths and residual risk

This implementation improves the FD boundary: receiving is opt-in per call (`ReceiveFiles`), descriptors attached to error replies are now closed by the library instead of being handed to callers that commonly discard errors, generated wrappers and internal call sites no longer have an ignored `Files` return to leak, the server's take-once `fdBox` closes untaken input descriptors, `AcceptFileDescriptors` remains off by default on both sides, and non-accepting listeners disable `SO_PASSRIGHTS` on Linux >= 6.16. Push-ownership is documented accurately: `server.go` and `docs/file-descriptors.md` require a `PushFile` descriptor to stay open until the reply is written, and `SendFiles`/`Files` documentation spells out that batched files must stay open until `Batch.Send` has written them. The HTTP bridge refuses `AcceptFileDescriptors` in its client config, and CI pins third-party actions by commit SHA with read-only `contents` permission.

Residual risk concentrates in the receive path: the reply destination is overwritten without draining (finding 2), and the pre-existing read-ahead descriptor budget scales with unlimited connections (finding 1).
