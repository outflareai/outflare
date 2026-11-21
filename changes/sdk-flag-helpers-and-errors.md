---
kind: improvement
area: analytics
---
The TypeScript and Python libraries got shortcuts for flags and errors on a server. `LocalFlags` answers for one flag with `variant`, `isEnabled` and `payload`; the event tracker's `exposed` counts a visitor in a flag's A/B test when your server shows them a version; and `captureException` sends an error to the Errors page with email addresses and keys taken out.
