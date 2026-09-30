# MetaTrash Private Spaces --- Implementation Plan

## Scope

This plan covers **human accounts** and the creation, routing, sharing,
and management of private spaces.

It deliberately does **not** change or specify:

-   MCP authentication
-   Agent identity
-   `start()`
-   Context tokens
-   Folder pinning
-   Agent authorization
-   Agent credential handling

Those systems will be integrated with private spaces in a **later
implementation stage**.

Throughout this document, **user** means a human MetaTrash account
holder.

The objective of this work is to establish the human account and
private-space model first, without redesigning the existing agent/MCP
architecture.

Human access to space content remains **read-only for all users**, including
owners. Account and membership management do not grant browser file editing.
Content permissions associated with invited users describe what their agents
will eventually be permitted to do; enforcement and the exact agent permission
model belong to the later agent integration, not these four stages.

## Roadmap relationship and delivery boundaries

This plan supersedes the combined private-dashboard bite in
[the human interface roadmap](human-interface.md). ZIP export, key generation
and rotation, deletion, username/slug changes, and remote Git remain on the
roadmap as separate deferred work. Ownership transfer is also deferred.

Each stage is a milestone, not necessarily one implementation patch. Keep
changes small, preserve before snapshots and incremental patches, and leave
builds and exhaustive testing to the owner.

Before expanding accounts, address F1 (login-email quota exhaustion) from
[the security review](security-review-2026-09-30.md) as a separate small fix.
Address F2 (private-space operation quota consumption before authentication)
before Stage 3. F3 (recent-history processing) remains a separate follow-up.

------------------------------------------------------------------------

# Stage 1 --- Database Setup and User Migration

**Status:** complete per owner confirmation. Implemented in 0.6.0 with the F1 fix; 0.6.1 corrected the MySQL logger. See [database setup, cutover, and recovery](account-database.md).

## Goal

Introduce MariaDB/MySQL as the control-plane database and migrate the
existing human user/account information into it.

MetaTrash already has MariaDB/MySQL available on the server, so that
database should be used rather than introducing another database
technology.

Git remains responsible for the actual MetaTrash content and its
revision history.

The database is responsible for account and access metadata.

## Work

### 1. Create the database schema

Create the initial schema required to represent existing human users.

Each user must have an immutable internal identifier such as:

``` text
user_id
```

This ID is the authoritative identity used for database relationships.

Email addresses, usernames, display names, and other changeable values
must not be used as relational identifiers.

### 2. Migrate existing users

Migrate the existing MetaTrash human users into the database.

The existing JSON account store already contains stable account IDs. Preserve
those exact IDs as `user_id`, along with verified normalized email, creation
time, and `maxPrivateSpaces`, including administrator overrides. Do not create
replacement identities. Retain the existing email normalization rules.

Provide a versioned schema and a repeatable migration that detects conflicting
records rather than overwriting them. Document a private backup, a maintenance
cutover with account writes stopped, and recovery instructions. After cutover,
MariaDB is the single authoritative account store; do not silently fall back
to stale JSON. Recovery must account for any accounts created after cutover.

The migration should preserve the existing authentication behaviour.

Existing users should continue to be able to log in using the same
mechanism they use today.

Keep email-code login and its existing session/code lifetimes. Sessions and
pending codes may remain in memory and expire on restart, as they do today;
database-backed sessions are not required for this bite. Session identity
should resolve to the immutable user ID, not use email as a relationship key.

### 3. Preserve existing behaviour

Stage 1 should introduce as little visible change as possible.

The purpose is to establish the database-backed human user model before
adding new account functionality.

## Stage 1 Result

At the end of Stage 1:

-   Existing human users are represented in MariaDB/MySQL.
-   Each user has a stable internal `user_id`.
-   Existing login behaviour still works.
-   No private-space functionality is required yet.

------------------------------------------------------------------------

# Stage 2 --- Human Login and Account Page Changes

**Status:** implemented locally in 0.7.0; owner build, schema upgrade, deployment,
and validation remain pending. See [public usernames and upgrade notes](public-usernames.md).
Stages 3–4 remain deferred.

## Goal

Extend the human account system so every user has a globally unique
public username suitable for use in URLs.

## Public Username

Add a public username to each human account.

For example:

``` text
dave
fred
alice
```

The public username is distinct from:

-   Email address
-   Display name
-   Internal `user_id`

### Username requirements

Usernames must be globally unique.

They should also be URL-safe.

A sensible initial rule would be to allow lowercase characters, numbers,
and selected separators such as hyphens.

For example:

``` text
dave
dave-c
fred123
```

Define a reserved-name list so usernames cannot collide with MetaTrash
application routes or special names.

Examples may include:

``` text
public
admin
api
mcp
login
logout
account
spaces
```

The exact validation rules and reserved-name list can be finalized
during implementation.

## Existing users

Existing users who do not yet have a public username should be prompted
to choose one from their account page.

Do not automatically derive the permanent username from their email
address.

Usernames are fixed once selected for this first implementation. Require one
before creating an owned space, but not for ordinary login or accepting an
invitation. Enforce normalized username uniqueness in the database as well as
the UI. Display-name changes can be a later independent feature.

## Account Page

Update the human account page to show and manage the user's account
information, including their public username.

The page should also provide placeholders/sections for functionality
that will be introduced in later stages, such as:

-   My Spaces
-   Invitations

These sections do not need to be functional until their corresponding
stages are implemented.

## Stage 2 Result

At the end of Stage 2:

-   Every human account can have a globally unique public username.
-   Username validation and reserved names are enforced.
-   Existing users can choose their username.
-   The human account page is ready to host space and invitation
    functionality.

------------------------------------------------------------------------

# Stage 3 --- Private Spaces and URLs

## Goal

Allow a logged-in human user to create and access their own private
spaces.

Sharing private spaces with other users is **not part of this stage**.

First establish that owned private spaces work correctly.

## Space Model

Create the database representation for spaces.

Each space should have an immutable internal identifier:

``` text
space_id
```

A space should record at least:

-   `space_id`
-   owner `user_id`
-   human-readable name
-   URL slug
-   visibility/type
-   creation metadata as appropriate

Database relationships must use `space_id` and `user_id`, not usernames
or URL slugs.

## Space Ownership

A logged-in user can create a private space.

The user who creates the space becomes its owner.

A user may own multiple private spaces.

Preserve the existing default allowance of one owned private space and
administrator overrides through `maxPrivateSpaces`. Enforce the allowance
server-side, including concurrent and in-progress creation requests. Joined
spaces do not consume the user's owned-space allowance.

`spaces.owner_user_id` is authoritative for ownership. Derive the owner's role
from it instead of storing a second independently editable owner membership.

## Space Names and Slugs

Space names do **not** need to be globally unique.

The owner's globally unique username provides the namespace.

A space slug only needs to be unique within that owner's spaces.

Enforce `(owner_user_id, slug)` uniqueness in the database. Slugs are fixed
after creation in this implementation; human-readable names need not be routing
identifiers.

Therefore these can both exist:

``` text
/spaces/dave/cloud-chamber
/spaces/fred/cloud-chamber
```

## URLs

Keep the existing public space URL:

``` text
/spaces/public
```

Private spaces use:

``` text
/spaces/{username}/{space-slug}
```

Examples:

``` text
/spaces/dave/cloud-chamber
/spaces/dave/zaptronics
/spaces/fred/cloud-chamber
```

The URL is human-readable routing information.

Internally, once the route has been resolved, MetaTrash should operate
using the immutable `user_id` and `space_id`.

Document links extend the space URL with the file path. Preserve the existing
public document routes and configured hosting prefix. Authorize every private
content, history, and supporting data request; private responses must use
no-store caching and must not enter public recent activity or listings.

## Account Page Integration

The user's account page should now show their private spaces.

Provide a way to:

-   Create a space
-   View owned spaces
-   Open a space

Rename and delete controls are deferred to keep this stage focused.

## Git Storage

Private-space content remains Git-backed.

This stage should extend the existing Git-backed space concept rather
than moving content into MariaDB.

MariaDB stores the space/account metadata.

Git stores the actual shared content and its history.

Use one repository per space, with new repository paths derived from immutable
`space_id`, never the username or slug. Preserve existing public and configured
private repositories without implicitly assigning them to human accounts.

### Provisioning and recovery

Database creation and Git provisioning cannot share one atomic transaction.
Track provisioning state, expose only ready spaces, and define retry/recovery
for interrupted creation. Reserve allowance during provisioning and release it
only after a failed attempt has been safely reconciled. Coordinate dynamic
space registration with concurrent readers and the existing write queue;
the current static maps cannot simply be mutated without synchronization.

Split Stage 3 into storage/provisioning, owner-authorized browsing/routing, and
account-page creation/listing bites as appropriate.

### Existing access boundary

Existing administrator-configured bearer-key spaces retain their current
behavior. Human login does not claim them. Newly created account-owned spaces
must reject REST/MCP access until the separate agent integration is implemented,
even if a caller knows their ID or URL. Enforce this in the shared service access
boundary without redesigning existing MCP authentication.

Initial spaces contain the service-provisioned content and offer read-only
human browsing. User content population through agents is intentionally deferred.

## Stage 3 Result

At the end of Stage 3:

-   Human users can create private spaces.
-   Users can see their spaces on their account page.
-   Private spaces have stable internal IDs.
-   Private spaces have human-readable URLs based on owner username and
    space slug.
-   `/spaces/public` continues to work.
-   Private-space content remains Git-backed.
-   Spaces are owner-only at this point.

------------------------------------------------------------------------

# Stage 4 --- Invitations and Human Membership

## Goal

Allow owners to invite other human MetaTrash users into their private
spaces and manage those memberships.

This stage introduces multi-user private spaces.

## Invitation Flow

The intended human workflow is:

1.  A space owner opens the space management page.
2.  The owner enters the email address of the person they want to
    invite.
3.  MetaTrash creates a pending invitation.
4.  The invited person logs in or creates a MetaTrash account.
5.  Their account page shows the pending invitation.
6.  They accept the invitation.
7.  They become a member of the private space.
8.  The space then appears in their account page.

The invitation system should be based on human accounts and memberships.

It should not involve MCP or agent credentials.

### Invitation rules

Match invitations only to the account's verified email using the same
normalization rules as login, then bind accepted membership to `user_id`.
For the initial bite, invitations appear on the account page after login;
automatic invitation emails are deferred, and the owner UI must make that clear.

Pending invitations expire after seven days and can be cancelled by the owner.
Allow at most one pending invitation per space and normalized email. Acceptance
must atomically confirm that the invitation is pending, unexpired, and addressed
to the signed-in account. Repeated acceptance must not create duplicate
memberships. An invitation cannot restore a suspended membership; restoration
is an explicit owner action. Removed members require a new invitation.

## Membership Model

Introduce a membership relationship between users and spaces.

Conceptually:

``` text
user
  ↓
space_membership
  ↓
space
```

A membership should record at least:

-   `user_id`
-   `space_id`
-   role
-   status
-   relevant creation/join metadata

Initial roles can remain simple:

``` text
owner
member
```

Initial membership states can also remain simple:

``` text
active
suspended
```

Enforce one membership per `(space_id, user_id)` for invited users. The owner
appears as `owner` in the UI through `spaces.owner_user_id`; owner suspension,
removal, and transfer are not member-management operations.

The `owner`/`member` roles describe human management authority. All humans
browse content read-only. Future read/write permissions for an invited user's
agents are a separate concern and must not be inferred from browser editing
capabilities or conflated with these management roles.

Removal can either be represented as a state or by deleting/archiving
the membership, depending on the desired audit model.

## Member Management

The owner should be able to see the humans who belong to the space.

For each member, the owner should be able to:

-   See their identity
-   See their role/status
-   Suspend their access
-   Restore suspended access
-   Remove them from the space

These actions apply only to that person's membership in the selected
space.

Check current membership on each authorized request. Suspension or removal
denies subsequent requests even if the human login session remains valid;
restoration re-enables that membership only. The later agent integration must
also honor membership revocation rather than relying on stale grants.

For example, if Fred belongs to both Dave's space and Alice's space,
Dave removing Fred from Dave's space must not affect Fred's membership
in Alice's space.

## Account Page

The account page should now show both:

-   Spaces the user owns
-   Spaces the user has joined

It should also show pending invitations requiring action.

## Stage 4 Result

At the end of Stage 4:

-   Space owners can invite other humans.
-   Invitees can accept invitations.
-   Users can belong to multiple private spaces.
-   Owners can view and manage members.
-   Membership can be suspended or removed independently for each space.
-   The account page provides the human-facing view of spaces and
    invitations.

------------------------------------------------------------------------

# Architectural Boundary

For this implementation, keep the following separation clear:

``` text
MariaDB / MySQL
    Human users
    Public usernames
    Spaces
    Ownership
    Invitations
    Memberships
    Membership state

Git
    Space content
    File history
    Revisions
    Rollback
```

Do not extend this work into agent-session architecture.

Specifically, the implementation of these four stages should not require
decisions about:

``` text
MCP credentials
start()
context tokens
folder pinning
agent identity
agent authorization
```

Once the human/private-space model is complete and stable, a separate
plan can define how agents discover, select, and authenticate against
the spaces available to a human account.

------------------------------------------------------------------------

# Implementation Order Summary

``` text
Stage 1
Database setup
    ↓
Existing human user migration
    ↓
Stable internal user IDs

Stage 2
Human account/login updates
    ↓
Globally unique public usernames
    ↓
Updated account page

Stage 3
Private space model
    ↓
Space creation
    ↓
/spaces/{username}/{space-slug}
    ↓
Owner-only private spaces

Stage 4
Invitations
    ↓
Memberships
    ↓
Member management
    ↓
Multi-user private spaces
```

This sequence intentionally establishes the human and private-space
foundation before any work is done on agent access to those spaces.

The first implementation bite after the F1 fix is Stage 1 only: schema,
identity-preserving migration, unchanged email-code login, and documented
backup/cutover/recovery. Later stages do not need to be implemented alongside it.
