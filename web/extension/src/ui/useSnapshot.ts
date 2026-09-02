/**
 * 页面侧的状态订阅：挂载时要一份 Snapshot，之后跟着 service worker 的广播走。
 * `send` 把每次请求的回包里的 Snapshot 也合并进来，所以点按钮后不用再单独刷新。
 */
import { useCallback, useEffect, useState } from 'react';
import { isSnapshotEvent, sendRequest, type Request, type Response } from '../shared/messages.ts';
import type { Snapshot } from '../shared/types.ts';

export interface SnapshotHandle {
  readonly snapshot: Snapshot | null;
  readonly loading: boolean;
  readonly busy: boolean;
  readonly error: { readonly code: string; readonly message: string } | null;
  send(request: Request): Promise<Response>;
  clearError(): void;
}

export function useSnapshot(): SnapshotHandle {
  const [snapshot, setSnapshot] = useState<Snapshot | null>(null);
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<SnapshotHandle['error']>(null);

  useEffect(() => {
    let alive = true;
    void sendRequest({ type: 'snapshot' }).then((res) => {
      if (!alive) return;
      if (res.ok) setSnapshot(res.snapshot);
      else setError(res.error);
      setLoading(false);
    });
    const listener = (message: unknown) => {
      if (isSnapshotEvent(message)) setSnapshot(message.snapshot);
    };
    chrome.runtime.onMessage.addListener(listener);
    return () => {
      alive = false;
      chrome.runtime.onMessage.removeListener(listener);
    };
  }, []);

  const send = useCallback(async (request: Request): Promise<Response> => {
    setBusy(true);
    setError(null);
    try {
      const res = await sendRequest(request);
      if (res.ok) setSnapshot(res.snapshot);
      else setError(res.error);
      return res;
    } finally {
      setBusy(false);
    }
  }, []);

  return { snapshot, loading, busy, error, send, clearError: () => setError(null) };
}
