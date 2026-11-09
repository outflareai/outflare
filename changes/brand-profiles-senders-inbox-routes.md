---
kind: improvement
area: workspace
---
Ideal customer profiles, senders, inbox tags and saved inbox views now belong to one brand each, under `/v1/brands/{brand}/`, and the old workspace routes answer 410. A profile shared by every brand is now copied into each brand, and two brands can use the same profile name.
