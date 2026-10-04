"use client";

import Image from "next/image";
import { useState } from "react";
import { isMemberPhotoUrl } from "@/lib/member-photo";

interface MemberPhotoProps {
  name: string;
  /** The member's photo_url; anything isMemberPhotoUrl rejects shows initials instead. */
  photoUrl?: string;
  /** "sm" on a scorecard card, "lg" in the member page's header. */
  size?: "sm" | "lg";
}

const SIZES = {
  sm: {
    box: "h-[60px] w-[50px] rounded-lg sm:h-[88px] sm:w-[72px]",
    text: "text-base sm:text-xl",
    width: 72,
    height: 88,
  },
  lg: {
    box: "h-[110px] w-[90px] rounded-xl sm:h-[150px] sm:w-[120px]",
    text: "text-2xl sm:text-3xl",
    width: 120,
    height: 150,
  },
} as const;

/**
 * The member's Congress.gov photo through next/image, so it's resized and served from our own
 * origin (the CSP's img-src stays 'self'). Initials stand in when there's no photo URL we may
 * load, and if it fails to load. Decorative: the name is always right beside it.
 */
export function MemberPhoto({ name, photoUrl, size = "sm" }: MemberPhotoProps) {
  const [failed, setFailed] = useState<string | null>(null);
  const { box, text, width, height } = SIZES[size];
  if (isMemberPhotoUrl(photoUrl) && failed !== photoUrl) {
    return (
      <Image
        src={photoUrl}
        alt=""
        width={width}
        height={height}
        className={`${box} shrink-0 bg-muted object-cover object-top`}
        onError={() => setFailed(photoUrl)}
      />
    );
  }
  return (
    <div
      aria-hidden="true"
      className={`${box} ${text} flex shrink-0 items-center justify-center bg-muted font-semibold text-muted-foreground`}
    >
      {initials(name)}
    </div>
  );
}

/** "AA" for "Ada Alvarez": the first letters of the first two words. */
function initials(name: string): string {
  return name
    .split(" ")
    .filter(Boolean)
    .map((n) => n[0])
    .join("")
    .slice(0, 2);
}
