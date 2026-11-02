---
kind: fix
area: partners
---
The Tolt import no longer marks commissions as paid just because they sit in a Tolt payout. Commissions in payouts Tolt hasn't paid yet (pending or "not eligible") keep their own status, and the next import run takes back any it marked paid before, removing the payouts it made for them.
