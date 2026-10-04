// The share card's image layout (#88), written for Satori (next/og): flexbox only, inline styles.
// Neutral colors only; parties appear as letters and votes as words, so the card reads in grayscale.

import type { CardCopy, CardText } from "@/lib/share";
import { SHARE_IMAGE_HEIGHT, SHARE_IMAGE_WIDTH, SHARE_METHODOLOGY_PATH } from "@/lib/share";

const colors = {
  background: "#ffffff",
  foreground: "#0f172a",
  muted: "#64748b",
  border: "#e2e8f0",
  surface: "#f1f5f9",
};

/**
 * Renders runs as one word per flex item, so a sentence with bold names wraps between words the
 * way inline text would (Satori lays out each child as a box).
 */
function Runs({ runs, fontSize }: { runs: CardText[]; fontSize: number }) {
  const words = runs.flatMap((run, i) =>
    run.text
      .split(" ")
      .filter(Boolean)
      .map((word, j) => ({ key: `${i}-${j}`, word, strong: run.strong }))
  );
  return (
    <div
      style={{
        display: "flex",
        flexWrap: "wrap",
        columnGap: fontSize * 0.26,
        fontSize,
        lineHeight: 1.2,
        color: colors.foreground,
      }}
    >
      {words.map(({ key, word, strong }) => (
        <span key={key} style={{ fontWeight: strong ? 700 : 400 }}>
          {word}
        </span>
      ))}
    </div>
  );
}

/**
 * The card. `logo` is the mascot as a data URI (`loadLogo()` in image.tsx): Satori can't fetch a
 * path, and a data URI keeps the render off the network.
 */
export function ShareCardImage({ copy, domain, logo }: { copy: CardCopy; domain?: string; logo: string }) {
  const headlineSize = copy.title ? 60 : 64;
  const methodology = copy.methodology ?? { label: "How votes are compared", path: SHARE_METHODOLOGY_PATH };
  return (
    <div
      style={{
        width: SHARE_IMAGE_WIDTH,
        height: SHARE_IMAGE_HEIGHT,
        display: "flex",
        flexDirection: "column",
        justifyContent: "space-between",
        padding: "56px 64px 44px",
        background: colors.background,
        fontFamily: "Geist",
        color: colors.foreground,
      }}
    >
      <div style={{ display: "flex", flexDirection: "column", gap: 24 }}>
        <div style={{ display: "flex", fontSize: 26, color: colors.muted, fontWeight: 700 }}>
          {copy.eyebrow}
        </div>
        {copy.title && (
          <div style={{ display: "flex", fontSize: 34, lineHeight: 1.25, fontWeight: 700 }}>{copy.title}</div>
        )}
        <Runs runs={copy.headline} fontSize={headlineSize} />
        {copy.detail && <Runs runs={copy.detail} fontSize={40} />}
      </div>
      <div style={{ display: "flex", flexDirection: "column", gap: 20 }}>
        <div style={{ display: "flex", fontSize: 24, lineHeight: 1.3, color: colors.muted }}>{copy.note}</div>
        <div
          style={{
            display: "flex",
            alignItems: "center",
            justifyContent: "space-between",
            borderTop: `2px solid ${colors.border}`,
            paddingTop: 20,
          }}
        >
          <div style={{ display: "flex", alignItems: "center", gap: 14 }}>
            {/* The scroll mascot, the site's one logo (#661, #706); the name beside it says what it is. */}
            {/* eslint-disable-next-line @next/next/no-img-element -- Satori draws a plain <img>, not next/image. */}
            <img src={logo} alt="" width={44} height={44} style={{ borderRadius: 10 }} />
            <div style={{ display: "flex", fontSize: 28, fontWeight: 700 }}>Just a Bill</div>
          </div>
          {domain && (
            <div style={{ display: "flex", fontSize: 24, color: colors.muted }}>
              {`${methodology.label}: ${domain}${methodology.path}`}
            </div>
          )}
        </div>
      </div>
    </div>
  );
}
