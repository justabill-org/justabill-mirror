# Security policy

## Reporting a vulnerability

Please report security problems privately, not in a public issue, pull request or discussion.

Use GitHub's [private vulnerability reporting](https://github.com/justabill-org/justabill/security/advisories/new)
(the repository's **Security** tab, then **Report a vulnerability**). Only the maintainers can see
the report. Include:

- what's affected (the web app, the API, the data pipeline, or this repository and its CI),
- steps to reproduce, or a proof of concept,
- the impact you expect, and whether you've seen it exploited.

## What to expect

- We aim to acknowledge a report within 3 business days, and we keep you updated in the advisory.
- We fix the supported version (the latest release and what's deployed) and publish the
  advisory once a release with the fix is out. Tell us if you'd like credit.
- Please give us a reasonable time to fix a problem before disclosing it, and don't access other
  people's data, degrade the service, or run automated scanners against production while you test.
  Good-faith research that follows this policy is welcome.

## In scope

- The code in this repository: the API (`api/`), the pipeline (`pipeline/`), the shared database
  module (`db/`), the telemetry module (`obs/`), the web app (`web/`), the scripts and the
  GitHub Actions workflows.
- Leaked credentials in the repository or its history.

Problems in upstream services (Congress.gov, GovInfo, the Census geocoder, Google Cloud, Vercel) go
to those providers. Wrong or outdated bill and vote data is a normal
[bug report](https://github.com/justabill-org/justabill/issues/new/choose), not a security issue.
