"use client";

import {
  createContext,
  useContext,
  useEffect,
  useId,
  useRef,
  useState,
  useSyncExternalStore,
  type HTMLAttributes,
  type ButtonHTMLAttributes,
  type KeyboardEvent,
} from "react";

interface TabsContextValue {
  activeTab: string;
  setActiveTab: (value: string) => void;
  /** Prefix for the tab and panel ids, so two Tabs on one page don't share ids. */
  idPrefix: string;
}

const TabsContext = createContext<TabsContextValue | null>(null);

function useTabs() {
  const context = useContext(TabsContext);
  if (!context) {
    throw new Error("Tabs components must be used within a Tabs provider");
  }
  return context;
}

interface TabsProps extends HTMLAttributes<HTMLDivElement> {
  defaultValue: string;
  value?: string;
  onValueChange?: (value: string) => void;
  /**
   * Tabs a link can open with a URL fragment naming one (`/bills/hr-119-1#votes`), scrolled into
   * view. Only tabs that are shown belong here: a fragment naming another opens the default.
   */
  linkable?: readonly string[];
}

function subscribeToHash(onChange: () => void): () => void {
  window.addEventListener("hashchange", onChange);
  return () => window.removeEventListener("hashchange", onChange);
}

/** The URL fragment without its `#`, read in the browser; the server render has none. */
function useHash(): string {
  return useSyncExternalStore(
    subscribeToHash,
    () => window.location.hash.slice(1),
    () => "",
  );
}

export function Tabs({
  defaultValue,
  value,
  onValueChange,
  linkable = [],
  className = "",
  children,
  ...props
}: TabsProps) {
  // A clicked tab, with the linked tab it was clicked under: a fragment that names another tab
  // later (a #votes link on the page, Back) takes over again.
  const [picked, setPicked] = useState<{ tab: string; linked: string | null } | null>(null);
  const idPrefix = useId();
  const hash = useHash();
  const linked = linkable.includes(hash) ? hash : null;
  const pickedTab = picked?.linked === linked ? picked.tab : null;
  const activeTab = value ?? pickedTab ?? linked ?? defaultValue;
  const container = useRef<HTMLDivElement>(null);

  // A tab opened by the fragment is scrolled to, as the browser would scroll to an anchor.
  useEffect(() => {
    if (linked) container.current?.scrollIntoView({ block: "start" });
  }, [linked]);

  const setActiveTab = (newValue: string) => {
    setPicked({ tab: newValue, linked });
    onValueChange?.(newValue);
  };

  return (
    <TabsContext.Provider value={{ activeTab, setActiveTab, idPrefix }}>
      <div ref={container} className={className} {...props}>
        {children}
      </div>
    </TabsContext.Provider>
  );
}

// Keys that move between tabs (WAI-ARIA APG tabs pattern, automatic activation): the
// arrows wrap around, Home and End jump to the ends.
const TAB_KEY_STEPS: Record<string, (index: number, count: number) => number> = {
  ArrowRight: (i, n) => (i + 1) % n,
  ArrowLeft: (i, n) => (i - 1 + n) % n,
  Home: () => 0,
  End: (_, n) => n - 1,
};

function handleTabListKeyDown(event: KeyboardEvent<HTMLDivElement>) {
  const step = TAB_KEY_STEPS[event.key];
  if (!step) return;
  const tabs = Array.from(
    event.currentTarget.querySelectorAll<HTMLButtonElement>('[role="tab"]:not(:disabled)')
  );
  const current = tabs.indexOf(event.target as HTMLButtonElement);
  if (current === -1) return;
  event.preventDefault();
  const next = tabs[step(current, tabs.length)];
  next.focus();
  next.click();
}

export function TabsList({
  className = "",
  children,
  onKeyDown,
  ...props
}: HTMLAttributes<HTMLDivElement>) {
  return (
    <div
      role="tablist"
      aria-orientation="horizontal"
      onKeyDown={(event) => {
        onKeyDown?.(event);
        if (!event.defaultPrevented) handleTabListKeyDown(event);
      }}
      className={`inline-flex h-10 items-center justify-center rounded-lg bg-muted p-1 text-muted-foreground ${className}`}
      {...props}
    >
      {children}
    </div>
  );
}

interface TabsTriggerProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  value: string;
}

export function TabsTrigger({
  value,
  className = "",
  children,
  ...props
}: TabsTriggerProps) {
  const { activeTab, setActiveTab, idPrefix } = useTabs();
  const isActive = activeTab === value;

  // Roving tabindex: Tab reaches only the selected tab; the arrow keys reach the others.
  // Inactive panels aren't rendered, so only the selected tab points at its panel.
  return (
    <button
      role="tab"
      type="button"
      id={`${idPrefix}-tab-${value}`}
      aria-selected={isActive}
      aria-controls={isActive ? `${idPrefix}-tabpanel-${value}` : undefined}
      tabIndex={isActive ? 0 : -1}
      data-state={isActive ? "active" : "inactive"}
      onClick={() => setActiveTab(value)}
      className={`inline-flex items-center justify-center whitespace-nowrap rounded-md px-3 py-1.5 text-sm font-medium ring-offset-background transition-all focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 disabled:pointer-events-none disabled:opacity-50 ${
        isActive
          ? "bg-background text-foreground shadow-sm"
          : "hover:bg-background/50 hover:text-foreground"
      } ${className}`}
      {...props}
    >
      {children}
    </button>
  );
}

interface TabsContentProps extends HTMLAttributes<HTMLDivElement> {
  value: string;
}

export function TabsContent({
  value,
  className = "",
  children,
  ...props
}: TabsContentProps) {
  const { activeTab, idPrefix } = useTabs();

  if (activeTab !== value) return null;

  return (
    <div
      role="tabpanel"
      id={`${idPrefix}-tabpanel-${value}`}
      aria-labelledby={`${idPrefix}-tab-${value}`}
      tabIndex={0}
      data-state={activeTab === value ? "active" : "inactive"}
      className={`mt-2 ring-offset-background focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 ${className}`}
      {...props}
    >
      {children}
    </div>
  );
}
