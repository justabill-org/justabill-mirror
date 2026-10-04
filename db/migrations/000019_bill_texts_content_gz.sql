-- A bill text's download, gzipped (#451). bill_texts.content is a STRING(MAX), which holds at most
-- 2,621,440 characters, so the biggest bills (the NDAA's 4 to 9 MB of XML) couldn't be stored and
-- were downloaded again on every sync-texts run. New rows keep the download here and, while it fits,
-- in content too; a longer text puts its plain text in content, or nothing when even that doesn't
-- fit. Readers prefer this column when content isn't the download. NULL on rows stored before it.
-- Expand/contract: content stays for the release before; dropping it is a later migration.
ALTER TABLE bill_texts ADD COLUMN content_gz BYTES(MAX);
