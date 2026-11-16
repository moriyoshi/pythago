# Isolation verdict

Moved verbatim from the "Durable lessons" section of `JOURNAL.md` (2026-11-17).

- All VTs on a carrier are one trust domain. Some per-OS-thread state is shared by every VT on
  the carrier:
  - glibc mutex ownership;
  - the OpenSSL error queue;
  - sigmask and affinity;
  - import `pkgcontext`.

  Untrusted or mutually distrusting code needs separate carriers or processes.
- Reusing thread states across unrelated threads leaks state: the async-exc target, context,
  dict, hooks and `ts->id`. Keep M=O until a fail-closed recycle exists.
