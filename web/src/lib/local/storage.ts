// A small versioned JSON store on localStorage, shared by the local vote and reps stores (#72, #214).
// It works with useSyncExternalStore: snapshots are stable objects, the server snapshot is empty,
// and other tabs' writes arrive through the `storage` event.

/** The parts of Web Storage the local stores use. */
export interface StorageLike {
  getItem(key: string): string | null;
  setItem(key: string, value: string): void;
  removeItem(key: string): void;
}

/**
 * Where a store's data lives: `device` is localStorage, `memory` is this page only (storage is
 * blocked or full), and `server` is the empty snapshot used before the browser takes over.
 */
export type Persistence = "device" | "memory" | "server";

/** What a store needs from the browser. `storage: null` means memory only. */
export interface LocalEnv {
  storage: StorageLike | null;
  events: Pick<EventTarget, "addEventListener" | "removeEventListener"> | null;
}

const PROBE_KEY = "jab.probe";

/** The browser's env, or null during a server render. localStorage that throws counts as missing. */
export function browserEnv(): LocalEnv | null {
  if (typeof window === "undefined") return null;
  let storage: StorageLike | null = null;
  try {
    const ls = window.localStorage;
    ls.setItem(PROBE_KEY, "1");
    ls.removeItem(PROBE_KEY);
    storage = ls;
  } catch {
    storage = null;
  }
  return { storage, events: window };
}

export interface Snapshot<T> {
  readonly value: T;
  readonly persistence: Persistence;
}

/** The result of reading a stored value: null rejects it, `lossy` keeps part of it. */
export type Parsed<T> = { value: T; lossy: boolean } | null;

export interface LocalStoreOptions<T> {
  key: string;
  /** Where unreadable data is moved, so a bad write or a future version never loses it. */
  corruptKey: string;
  empty: T;
  parse: (data: unknown) => Parsed<T>;
  serialize: (value: T) => unknown;
  env?: () => LocalEnv | null;
}

export interface LocalStore<T> {
  subscribe(listener: () => void): () => void;
  getSnapshot(): Snapshot<T>;
  getServerSnapshot(): Snapshot<T>;
  update(fn: (value: T) => T): Snapshot<T>;
  /** The most recent value moved aside to the corrupt key, if any. */
  readCorrupt(): string | null;
}

export function createLocalStore<T>(opts: LocalStoreOptions<T>): LocalStore<T> {
  const serverSnapshot: Snapshot<T> = Object.freeze({ value: opts.empty, persistence: "server" });
  const listeners = new Set<() => void>();
  let env: LocalEnv | null = null;
  let snapshot: Snapshot<T> | null = null;

  const notify = () => listeners.forEach((l) => l());

  // Moves raw aside without overwriting an earlier one. If that fails, the store stays in memory
  // so it never writes over data it couldn't keep.
  function moveAside(storage: StorageLike, raw: string): boolean {
    try {
      const existing = storage.getItem(opts.corruptKey);
      const aside = existing === null || existing === raw ? opts.corruptKey : `${opts.corruptKey}.${Date.now()}`;
      storage.setItem(aside, raw);
      return true;
    } catch {
      return false;
    }
  }

  function read(): Snapshot<T> {
    const storage = env?.storage;
    if (!storage) return { value: opts.empty, persistence: "memory" };
    let raw: string | null;
    try {
      raw = storage.getItem(opts.key);
    } catch {
      return { value: opts.empty, persistence: "memory" };
    }
    if (raw === null) return { value: opts.empty, persistence: "device" };

    let parsed: Parsed<T> = null;
    try {
      parsed = opts.parse(JSON.parse(raw));
    } catch {
      parsed = null;
    }
    if (parsed && !parsed.lossy) return { value: parsed.value, persistence: "device" };

    const value = parsed ? parsed.value : opts.empty;
    if (!moveAside(storage, raw)) return { value, persistence: "memory" };
    return write({ value, persistence: "device" });
  }

  function write(next: Snapshot<T>): Snapshot<T> {
    const storage = env?.storage;
    if (!storage || next.persistence !== "device") return next;
    try {
      storage.setItem(opts.key, JSON.stringify(opts.serialize(next.value)));
      return next;
    } catch {
      return { value: next.value, persistence: "memory" };
    }
  }

  function load(): Snapshot<T> {
    if (!snapshot) {
      env = (opts.env ?? browserEnv)();
      snapshot = env ? read() : serverSnapshot;
    }
    return snapshot;
  }

  function onStorage(e: Event) {
    const { key } = e as StorageEvent;
    // key is null when another tab called localStorage.clear().
    if (key !== opts.key && key !== null) return;
    if (snapshot?.persistence !== "device") return;
    snapshot = read();
    notify();
  }

  return {
    subscribe(listener) {
      load();
      listeners.add(listener);
      if (listeners.size === 1) env?.events?.addEventListener("storage", onStorage);
      return () => {
        listeners.delete(listener);
        if (listeners.size === 0) env?.events?.removeEventListener("storage", onStorage);
      };
    },
    getSnapshot: load,
    getServerSnapshot: () => serverSnapshot,
    update(fn) {
      const current = load();
      if (current === serverSnapshot) return current;
      snapshot = write({ value: fn(current.value), persistence: current.persistence });
      notify();
      return snapshot;
    },
    readCorrupt() {
      load();
      try {
        return env?.storage?.getItem(opts.corruptKey) ?? null;
      } catch {
        return null;
      }
    },
  };
}
