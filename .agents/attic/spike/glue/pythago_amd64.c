/*
 * Workaround (linux/amd64): bao rejects SHN_COMMON symbols, and OpenSSL's x86_64cpuid.s
 * declares OPENSSL_ia32cap_P with .comm (40 bytes, hidden). This definition resolves the
 * common symbol in bao's partial link; OpenSSL fills it in at startup as before.
 */
#if defined(__x86_64__)
__attribute__((visibility("hidden"), aligned(16))) unsigned int OPENSSL_ia32cap_P[10];
#endif

/*
 * Workaround (linux/amd64): bao rejects references to missing weak symbols in the
 * `cmp $0, sym@GOTPCREL(%rip)` form (addend -5). libzstd tests its optional trace hooks
 * that way. These definitions behave like absent hooks: begin returns 0 ("not tracing"),
 * so zstd never calls end.
 */
#if defined(__x86_64__)
typedef unsigned long long ZSTD_TraceCtx;
ZSTD_TraceCtx ZSTD_trace_compress_begin(const void *cctx) { (void)cctx; return 0; }
void ZSTD_trace_compress_end(ZSTD_TraceCtx ctx, const void *trace) { (void)ctx; (void)trace; }
ZSTD_TraceCtx ZSTD_trace_decompress_begin(const void *dctx) { (void)dctx; return 0; }
void ZSTD_trace_decompress_end(ZSTD_TraceCtx ctx, const void *trace) { (void)ctx; (void)trace; }
#endif
