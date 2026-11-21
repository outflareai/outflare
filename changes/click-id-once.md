---
kind: fix
area: paid-ads
---
Tracker destinations now get the click id (ofclk) exactly once, even when a lander or offer URL says ofclk={click_id}, and a URL with any other ofclk is refused with the reason. Offers have a "Click id delivery" setting, so a page of your own no longer shows "Missing click id".
