# Roles

Everyone in a workspace has one role, and the role decides what they can do. Every workspace starts with four:

| Role | What it's for |
| --- | --- |
| **Owner** | Everything, including removing people and managing owners. Fixed: it can't be changed. |
| **Admin** | Everything except removing people or reducing their access. The role for your own team. |
| **Member** | All the marketing work, and connecting tools. No settings, people, API tokens or audit log. Stored and sent in the API as `operator`. |
| **Viewer** | Sees everything, changes nothing. |

Change what Admin, Member or Viewer allows, or add roles of your own (a "Content writer" with only SEO and Social, say), under **Settings → Members → Roles**. Each role is a list of areas set to None, View or Edit, plus switches for actions that need their own say: sending email, posting to social accounts, approving missions and spending, the unsubscribe list, shared fields, webhooks, settings, API tokens, the audit log, inviting people, removing people and editing roles. A changed starting role can be reset to its defaults.

## The rules

- **You can't give what you don't have.** Every permission in a role you create or edit, or a role you give someone, must be one you hold. An admin can never make a role that removes people.
- **You can't change someone who can do more than you.** You can only change or remove a person whose role is entirely within yours. Only owners make, change or remove owners, and a workspace always keeps at least one.
- **Taking access away is its own permission.** Removing someone, moving them to a role that can do less, taking brands away from them, or taking a permission off a role people are on needs **Remove people** (`members.remove`). Only owners have it by default. An owner can switch it on for Admin or any other role.
- **Changes apply at once.** Editing a role changes what everyone on it can do straight away. API tokens they made lose the permissions the role no longer has, and stop working when none are left. Every change is in the audit log (`role.created`, `role.updated`, `role.deleted`, `member.role_changed`).
- **A role in use can't just disappear.** Deleting a role asks where its people and pending invites should move.
- **Anyone can leave** a workspace under Settings → Members (`POST /v1/workspaces/{ws}/leave`), except its last owner.

## Inviting people

Owners and admins invite people under Settings → Members, picking any role they could give. Admin is the default. Owners can invite owners. For a role without workspace-wide permissions (settings, people, roles, API tokens or the audit log), you can also keep the person to some brands.

## API tokens and AI apps

API tokens never hold more than the person who made them, and never `members.remove` or `roles.manage`: removing people and changing roles is always done by a signed-in person. Connected AI apps (Claude, ChatGPT) never get people, roles, tokens, workspace settings or approvals, whatever the role allows.

## In the API

- `GET /v1/workspaces/{ws}/roles` lists the roles, with what each allows (`scopes`) and how many people and pending invites use it. MCP: `list_roles`.
- `POST /roles`, `PATCH /roles/{role}` and `DELETE /roles/{role}` (`?move_to=` when it's in use) need `roles.manage`. `{role}` is a role's id, or `admin`, `operator` (or `member`) or `viewer`.
- Members and invites take `role` (a starting role) or `role_id` (one of the workspace's own roles). They return `role_id` and `role_name`. `role` then holds the starting role nearest below the custom one, which is what a version without custom roles reads.
- `GET /v1/workspaces/{ws}` and `/v1/me` return your `scopes` in each workspace.
