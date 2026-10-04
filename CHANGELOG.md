# Changelog

## 0.10.0 (2026-10-04)


### Features

* **web:** link each amendment on a bill page to its Congress.gov record (#895) (cc16dee)


### Bug Fixes

* **api:** refuse a malformed congress or district filter on every list route (#896) (98d1466)
* **release:** let Publish source push the public CI workflow and not fail the run (#891) (f26ef1c)
* restore main CI (e2e-flakes workers overwrite each other's records) (#888) (3534ac3)


### Performance Improvements

* **db:** read the summary queue and diff sweep as flat sets, not per bill (#892) (a1f49eb)

## 0.9.0 (2026-10-04)


### ⚠ BREAKING CHANGES

* **api:** drop the deprecated why_it_matters summary alias (#862)

### Features

* **release:** import a public contribution as a held pull request (#874) (5ca60f9)
* **release:** mark released contributions merged and credit their authors (#887) (7487076)
* **release:** publish each release's snapshot to the public repo, with its CI and issue forms (#861) (5324497)


### Bug Fixes

* **api:** 400 and 404 on the bill list, member and favorite routes, with handler tests (#880) (dd85144)
* **export:** keep the maintainer's identity and production's names out of the public export (#886) (b096d32)
* **pipeline:** retry load-uscode hourly and report uscode.house.gov maintenance as unavailable (#875) (fd1b7ef)
* the small findings of the v0.6.0..main release review (#879) (360cdd5)


### Performance Improvements

* **db:** index the bill list by current status for its views and counts (#878) (d5dd242)
* **pipeline:** run the pipeline's Spanner queries at low priority (#876) (d6ce56f)


### Code Refactoring

* **api:** drop the deprecated why_it_matters summary alias (#862) (d496fc5)

## 0.8.0 (2026-10-04)


### Features

* **api:** serve a CRA resolution's disapproved rule on GET /bills/{id} (#833) (1180ae7)
* **api:** serve each congress's bills past committee and their status on GET /bill-statuses (#856) (6c833e2)
* **dev:** add a devcontainer.json over the compose dev service (#847) (8a7be8d)
* **dev:** run the pre-commit hook's checks in the dev container when the host lacks the toolchain (#846) (094ff12)
* **scripts:** classify every file for the public export, and check it on every PR (#855) (ad1ebf4)
* **web:** a My votes page that stays clear with hundreds of votes (#850) (9ccab37)
* **web:** a policy area filter on /bills and one filter model shared with /vote (#804) (2be8f7b)
* **web:** a scorecard congress switch with vote counts, under Your representatives (#849) (7ba0944)
* **web:** deal every bill under the /bills filters on /vote (#851) (dd73758)
* **web:** one list call per /bills view, latest-action sort, one counts call and CRS card lines (#848) (4633e35)
* **web:** show the rule a CRA resolution disapproves on its bill page (#859) (f98abdb)


### Bug Fixes

* **api:** run without a cache when REDIS_URL is set but empty (#840) (dbad609)
* **db:** order a member's same-day recent votes by session, roll number and vote ID (#832) (37284dc)

## 0.7.0 (2026-10-03)


### Features

* **api:** carry each bill's CRS summary lead on GET /bills?include=crs_summary (#802) (7997266)
* **api:** connect with the database role named in SPANNER_DATABASE_ROLE (#690) (da995a8)
* **db:** explain enacted laws first in the law-change queue (#780) (f1bc76d)
* **db:** read the vote card's law number from bills.laws (#765) (4726085)
* **dev:** run the Playwright smoke tests from containers with task docker:e2e (#835) (d0349cb)
* **dev:** run the tests and linters in a dev container with task docker:* (#775) (d66ab59)
* **pipeline:** summarize CRA resolutions with the rule they disapprove and re-summarize on change (#750) (07eb6c3)
* **web:** a member page with photo, terms and the bill behind each recent vote (#795) (a9df85a)
* **web:** scorecard voice, ink bill numbers and progress, rename --accent to --link (#798) (12374e2)
* **web:** settings and onboarding district form with autofill fields and use my location (#749) (8971692)
* **web:** share a bill from a Share this bill sheet on its page (#813) (ea16c1c)
* **web:** share a bill from the /bills cards and the /vote card (#815) (cace2bd)
* **web:** show only the sign-in providers that are turned on (#809) (424c4ed)


### Bug Fixes

* **api:** log a cache key's prefix, not the visitor's search text, on cache errors (#774) (d43a510)
* **db:** bound the law-changes and companion-votes reads that timed out the API (#801) (9f1d790)
* **pipeline:** skip batch summary lines for bills no longer held for that text (#689) (7b23b1a)
* restore main CI (member page still used the renamed --accent color) (#841) (efead8f)
* **task:** pass the Census and OTLP stub ports through task e2e and e2e:sign-in (#778) (127bbf4)
* **web:** load Firebase only for visitors who sign in, so no gapi loads signed out (#779) (95b6d00)
* **web:** reduce API URLs in browser traces to route templates (#769) (2dc3101)
* **web:** say an original measure began in committee, not that its referral date is missing (#808) (e7304bf)
* **web:** stop saying the code is public or sending people to GitHub (#807) (af6e7b2)


### Performance Improvements

* **db:** page the bill list from ordered, covering indexes (#773) (66b76b7)
* **db:** read the text queues from idx_bill_texts_version and drop unused indexes (#836) (1dd939e)

## 0.6.0 (2026-10-03)


### Features

* **api:** count the bill list by status in one call with GET /bills/counts (#742) (1bd5f0e)
* **web:** a card per representative on the scorecard, with photo, party and the bills compared (#684) (5373bcf)
* **web:** a site-wide design pass: plain voice, a home page that shows the newest law, one bar (#732) (1c059c9)
* **web:** show what a bill does and how it passed on each /vote card (#682) (a18fab1)


### Bug Fixes

* **web:** call the api service from the compose web container's server renders (#685) (7d51429)
* **web:** keep the sign-in ?next= path on this site when it holds a tab or newline (#768) (271db01)

## 0.5.0 (2026-10-03)


### Features

* **api:** filter GET /bills by policy area and list every policy area (#734) (67fc1d8)
* **api:** filter GET /bills by several statuses and sort by latest action date (#720) (a35154d)
* **api:** look up representatives by coordinates in POST /reps (#723) (bba5363)
* **api:** serve each bill's vote card facts on GET /bills?include=card (#740) (566cf48)
* **db:** read a page of bills' vote card facts in two queries (#722) (f261859)
* **pipeline:** match CRA resolutions to the Federal Register documents they disapprove (#719) (b9ae86f)
* **pipeline:** store a law's number from Congress.gov and serve it as bill.laws (#735) (c5bf9db)
* **web:** a My votes page to see, change and remove your votes (#743) (e75ec41)
* **web:** bill progress with dates, merged key actions, one summary and folding cards (#680) (c42ba33)
* **web:** find your representatives by location or autofilled address fields (#686) (1a60c14)
* **web:** links to My votes from the vote card, deck and settings, and the device tools move there (#745) (d607725)
* **web:** show the example data on Vercel previews while the API is private (#718) (3147a83)
* **web:** start the A/A run on /vote (#724) (92fe3a9)


### Bug Fixes

* **api:** keep the clock out of the reps-lookup log address check (#701) (f1c221b)
* **pipeline:** classify bill stages by action code, and re-derive stored status history (#699) (abcfeb4)
* **web:** fit pagination on phones with a Page N of M row below sm (#727) (7164232)

## 0.4.0 (2026-10-03)


### Features

* **api:** count failed web function requests and census drain lines in vercel-drain (#602) (c064ce6)
* **api:** serve summary.who_it_affects and deprecate the why_it_matters alias (#603) (2ebff3c)
* **api:** show statutory notes as US Code material with their explanation (#615) (df25f85)
* **db:** add the CRA rule tables and their store methods (#644) (e8ad617)
* **db:** create the api database role and guard its grants (#638) (1add328)
* **pipeline:** add the CRA resolution parser (#645) (c9220c0)
* **pipeline:** store empty diffs so the sweep stops re-diffing them (#604) (3b2f0aa)
* **pipeline:** summarize every law and bill that passed a chamber (#691) (bcdc670)
* **web:** add prototype rules: scope check, sample data and the prototyping skill (#595) (5292a62)
* **web:** bills page with quick views, one-line summaries and progress (#687) (8b5c5d8)
* **web:** experiments foundation: arm assignment, counters, readout and the Privacy page (#696) (546e788)
* **web:** filter the /vote deck by how far a bill got, laws first (#681) (194a389)
* **web:** let voters remove a vote on the bill page (#613) (9203433)
* **web:** make "Unvoted only" on /bills work for signed-in users (#584) (d026d4a)
* **web:** one final text for passed bills, with each chamber's versions and companions (#683) (6b69c81)
* **web:** one header and logo on every page (#677) (adeef1b)
* **web:** share a published aggregate cell as a card (share cards 4/4) (#592) (beb0a89)


### Bug Fixes

* **api:** bound the cost of unauthenticated search (#629) (f938664)
* **api:** cap vote imports, refuse revoked tokens on POST /me, add vote removal and full export (#596) (0ccf3cb)
* **api:** count the web server's calls per visitor (#637) (1033326)
* **api:** keep the Redis cache when the first ping fails (#578) (30337cd)
* **api:** keep the Redis password out of REDIS_URL parse errors (#601) (050ff93)
* **api:** reject PATCH /me states and districts that aren't House seats (#631) (29469af)
* **pipeline:** continue a timed-out sync-bills run instead of starting over (#611) (856d7b9)
* **web:** an unknown bill ID costs at most one API call (#692) (c61ebe5)
* **web:** pass the visitor's IP on filtered /bills lists (#654) (2526641)
* **web:** say the daily vote limit is reached instead of offering a retry (#655) (803826a)
* **web:** store device vote times in RFC 3339 so account import accepts them (#657) (4c7bb5b)

## 0.3.0 (2026-10-01)


### Features

* **web:** show how Just a Bill users voted on bill pages and the scorecard (#560) (a2e6f28)


### Bug Fixes

* **pipeline:** find House roll calls from the roll files now that the Clerk's index pages are gone (#587) (9ac8fce)
* **pipeline:** write law references for texts stored by sync-govinfo (#585) (97cb063)

## 0.2.0 (2026-10-01)


### Features

* **api:** rate-limit signed-in routes per account, not per IP (#557) (c2410ce)
* **obs:** count law-change explanation calls in justabill.law_change.results (#568) (e98d334)
* **pipeline:** excerpt every cited subsection of a long US Code section (#572) (702d039)
* **pipeline:** submit, poll and import Vertex AI summary batches (#520) (2cb7522)


### Bug Fixes

* **api:** make the /bills chamber filter, default order and vote order do what they say (#555) (679ad94)
* **db:** delete a pruned text version's law references (#552) (a798007)
* **pipeline:** close the health listener in Shutdown even before Serve starts (#562) (9154c2e)
* **pipeline:** don't ask the law explainer about nonusc: references (#574) (e2d4926)
* **pipeline:** give a leader a second renewal before the lease loss threshold (#569) (594d72f)
* **pipeline:** write each roll call and its member votes in one commit (#576) (1a8902d)
* **web:** always build production deployments in vercel-ignore.sh (#532) (c69d507)

## 0.1.0 (2026-10-01)


### Features

* **api:** count App Check verifications by result and mode (#472) (5044bd4)
* **api:** include bill titles in GET /me/votes (#561) (56a775d)
* **api:** private access mode and secrets from mounted files (#501) (73ad10a)
* **api:** serve a bill's law changes and US Code section text (#522) (ed70173)
* **api:** serve published aggregates and rep alignment behind AGGREGATES_PUBLIC (#558) (c8fac10)
* **db:** add an is_empty flag to bill_text_diffs and filter it from every reader (#504) (e9fbdcc)
* **db:** add summary batches, attempt request types and batch holds (#490) (90c79d3)
* **db:** store bill texts of any size gzipped in content_gz (#523) (ef8cfdb)
* **db:** task db:rebase merges main and renumbers colliding migrations (#511) (7eacfa5)
* **pipeline:** add the aggregates CLI (report, hold, release, exclude) (#536) (49d4794)
* **pipeline:** add the hourly aggregate-votes job with publication rules and holds (#518) (b7614e6)
* **pipeline:** archive upstream responses to GCS during the load (#437) (f20f942)
* **pipeline:** clear the API's cached bill copies before revalidating (#464) (3b3adc9)
* **pipeline:** explain each bill's changes to current law with one Gemini call (#479) (f145ca0)
* **pipeline:** give the AI prompt the CRS summary (bill-v3) and re-summarize on new CRS (#524) (2d47be1)
* **pipeline:** match diff sections by position across versions and recompute stored diffs (#500) (b7750d6)
* **pipeline:** parse the whole bill text at any depth and reparse stored texts (#462) (88ceabc)
* **pipeline:** read REDIS_URL_FILE for the API cache clearer (#534) (a2b1613)
* **pipeline:** record diff summary attempts and count them toward the daily cap (#483) (91a9cdc)
* **pipeline:** sync CRS summaries incrementally from /summaries/{congress} (#468) (0ee01a3)
* **web:** account votes, onboarding with vote import, and data download and deletion (#496) (68195b3)
* **web:** add a Sharing section to Privacy and link share cards to Methodology (#469) (6f5a9f5)
* **web:** follow bills when signed in and list them in settings (#505) (12cfed8)
* **web:** record http.server.request.duration from the Next.js server span (#498) (9ed9e32)
* **web:** send App Check tokens on votes and vote imports (aggregates 4/8) (#499) (0c50e06)
* **web:** show a bill's changes to current law on the bill page (#537) (621c40a)
* **web:** show the CRS summary on the bill page and serve it from GET /bills/{id} (#516) (10887d3)
* **web:** sign in with Google, Apple and Microsoft through Firebase (social auth 5/7) (#485) (29c4a56)
* **web:** trace the browser and report its errors through a same-origin OTLP relay (#466) (f9524a6)


### Bug Fixes

* **api:** read bill search as plain words and answer 400 on a bad search (#542) (72b0c59)
* **ci:** run the required Web E2E check whenever its suites run (#553) (65dffcd)
* **db:** break sort ties in offset-paged lists so pages don't repeat or skip rows (#547) (9c41e37)
* **db:** delete a recomputed diff's summary attempt with its summary (#551) (1d0397f)
* **db:** leave members whose term ended out of the scorecard (#546) (d3a2ed8)
* restore main CI (@grpc/grpc-js high-severity advisory in npm audit) (#543) (c25df69)
* restore main CI (flaky 5s timeout on web axe test) (#535) (15caf18)
* **web:** keep tests' git out of the repo a hook runs in (#540) (a6624eb)
* **web:** set the dev cookie Secure and strip tags before decoding entities (#476) (190e5c2)
* **web:** style the last button-links as links instead of nesting buttons (#491) (d5b9eda)


### Performance Improvements

* **api:** diff metadata only on bill pages, gzip JSON, no raw XML beside sections (#512) (8c06d4b)
