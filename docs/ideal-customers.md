# Ideal customers

An ideal customer profile says who you sell to, once. Lead list fit scores and the lead finder use the same profile, so the same customer is never typed twice in different words.

A profile has:

- a **name**, such as "Series A fintech, AU";
- a **description** in words: who they are and why they buy. AI steps read it;
- **filters** in the same vocabulary as lead searches: job titles, seniority, industries, company size (employee ranges), locations and keywords;
- an optional **brand**, when it describes one brand's customers. A profile without a brand fits every brand.

Find them under **Company → Ideal customers**, at `/v1/workspaces/{ws}/ideal-customer-profiles`, or with the MCP tools `list_ideal_customer_profiles`, `get_ideal_customer_profile`, `create_ideal_customer_profile` and `update_ideal_customer_profile`. Reading needs `projects.read`; making and changing them needs `projects.manage`.

## Profiles and lead lists

A lead list's `profile_id` names the profile its leads are scored against. Archiving a profile hides it from the list; lists that use it keep scoring against it. A change to a profile's description or filters bumps its `version`, so a lead list run can say which version it scored against.

```sh
curl -X POST "localhost:8080/v1/workspaces/$WS/ideal-customer-profiles" \
  -H "Authorization: Bearer $OUTFLARE_TOKEN" -H "Content-Type: application/json" \
  -d '{"name": "Series A fintech, AU", "description": "Fintechs that just raised and need pipeline.",
       "filters": {"industries": ["Financial Services"], "employee_ranges": ["51-200"], "locations": ["Australia"]}}'

curl -X PATCH "localhost:8080/v1/workspaces/$WS/lead-lists/$LIST" \
  -H "Authorization: Bearer $OUTFLARE_TOKEN" -H "Content-Type: application/json" -d '{"profile_id": "'$PROFILE'"}'
```

Each change is an audit event and a product event: `ideal_customer_profile.created`, `.updated`, `.archived` and `.restored`.
