---
kind: fix
area: social
---
Trustpilot and Capterra review pages are now read as Geonode jobs that can take as long as the page needs, without sending a page twice. A page behind Cloudflare is tried again the same day instead of waiting a day, Capterra no longer asks for a second page that doesn't exist, and a first read that stops partway reads the history again.
