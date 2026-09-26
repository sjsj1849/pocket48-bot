export async function withHardTimeout(promise, timeoutMs, label, onTimeout) {
  let timer;
  try {
    return await Promise.race([
      promise,
      new Promise((_, reject) => {
        timer = setTimeout(async () => {
          try {
            await onTimeout?.();
          } catch {}
          reject(new Error(`${label} hard timeout after ${timeoutMs}ms`));
        }, timeoutMs);
      }),
    ]);
  } finally {
    clearTimeout(timer);
  }
}
