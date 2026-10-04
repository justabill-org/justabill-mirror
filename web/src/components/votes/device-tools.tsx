"use client";

import { useRef, useState } from "react";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { downloadJson } from "@/lib/download";
import { VoteImportError, type LocalVoteStore } from "@/lib/local/votes";
import { localVotes } from "@/lib/votes/backend";

export interface DeviceToolsProps {
  /** The browser's vote store; defaults to the page-wide one. */
  store?: LocalVoteStore;
  /** For tests; defaults to a file download. */
  save?: (text: string, filename: string) => void;
  /** Called after an import or a clear changed the stored votes. */
  onChange?: () => void;
}

/**
 * The signed-out tools for the votes kept in this browser (#215, moved to My votes in #739):
 * download them, import a downloaded file, clear them, and download any saved votes that couldn't
 * be read. All of it stays in the browser: nothing here sends a request.
 */
export function DeviceTools({ store = localVotes(), save = downloadJson, onChange }: DeviceToolsProps) {
  const fileInput = useRef<HTMLInputElement>(null);
  const [message, setMessage] = useState<string | null>(null);
  const corrupt = store.readCorrupt();

  const handleImport = async (e: React.ChangeEvent<HTMLInputElement>) => {
    const file = e.target.files?.[0];
    e.target.value = "";
    if (!file) return;
    try {
      const r = store.importVotes(await file.text());
      setMessage(`Imported ${r.added} new and ${r.updated} updated votes (${r.unchanged} unchanged).`);
      onChange?.();
    } catch (err) {
      setMessage(err instanceof VoteImportError ? err.message : "That file couldn't be read.");
    }
  };

  const handleClear = () => {
    if (window.confirm("Clear every vote saved on this device? This can't be undone.")) {
      store.clearAll();
      setMessage("Your votes on this device were cleared.");
      onChange?.();
    }
  };

  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-lg">Your votes on this device</CardTitle>
        <CardDescription>
          Anyone using this browser can see them. Download a copy to keep them or to import them on another
          device.
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-3">
        <div className="flex flex-wrap gap-2">
          <Button
            variant="outline"
            size="sm"
            onClick={() => save(JSON.stringify(store.exportVotes(), null, 2), "just-a-bill-votes.json")}
          >
            Download my votes
          </Button>
          <Button variant="outline" size="sm" onClick={() => fileInput.current?.click()}>
            Import votes
          </Button>
          <Button variant="outline" size="sm" className="text-destructive" onClick={handleClear}>
            Clear my votes
          </Button>
          <input
            ref={fileInput}
            type="file"
            accept="application/json,.json"
            className="hidden"
            aria-label="Votes file to import"
            onChange={handleImport}
          />
        </div>
        {corrupt && (
          <p className="text-sm text-muted-foreground">
            Some saved votes couldn&apos;t be read.{" "}
            <button type="button" className="underline" onClick={() => save(corrupt, "just-a-bill-votes-unreadable.json")}>
              Download them
            </button>{" "}
            to keep a copy.
          </p>
        )}
        {message && (
          <p role="status" className="text-sm text-foreground">
            {message}
          </p>
        )}
      </CardContent>
    </Card>
  );
}
