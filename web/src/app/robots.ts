import type { MetadataRoute } from "next";
import { robotsRules } from "@/lib/seo";

// robots.txt (#86). Don't Disallow /share/: link-preview crawlers (X, Facebook, Slack, iMessage)
// honor robots.txt, and a blocked share page or image.png loses its card. Share pages keep out of
// search with noindex instead (#88, wiki "Share Cards"). Non-production deployments disallow all.
export default function robots(): MetadataRoute.Robots {
  return robotsRules();
}
