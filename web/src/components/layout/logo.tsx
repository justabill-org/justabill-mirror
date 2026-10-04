import Image from "next/image";
import Link from "next/link";

// The one brand mark (#661): the scroll mascot and the name, linking home. The header and the
// footer both use it, so every page shows the same logo.

const SIZES = {
  md: { px: 32, image: "h-8 w-8 rounded-lg", text: "text-lg font-semibold tracking-tight", gap: "gap-2" },
  sm: { px: 20, image: "h-5 w-5 rounded", text: "font-medium text-foreground", gap: "gap-1.5" },
} as const;

interface LogoProps {
  /** md in the header, sm in the footer. */
  size?: keyof typeof SIZES;
  className?: string;
}

export function Logo({ size = "md", className = "" }: LogoProps) {
  const s = SIZES[size];
  return (
    <Link href="/" className={`flex items-center ${s.gap} ${className}`}>
      {/* The name beside it says what the link is, so the image is decoration. */}
      <Image src="/bill-icon.jpg" alt="" width={s.px} height={s.px} className={s.image} />
      <span className={s.text}>Just a Bill</span>
    </Link>
  );
}
