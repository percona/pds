# UI rules (`apps/ui/`)

Read the root [AGENTS.md](../../AGENTS.md) first: the workflow, the gates, the
customer copy rules and the design locks live there. This file covers
`@pds/ui`.

## Structure

- Feature code lives in `src/features/<name>/`: its components, hooks, the
  API calls in `api.ts`, its types in `types.ts`, and a public surface in
  `index.ts`. Import another feature through its `index.ts`. A few older
  imports reach into another feature's `api.ts`, `types.ts` or components;
  do not add more.
- Shared primitives live in `src/components/ui/`, the app frame in
  `src/components/layout/`, pages in `src/pages/`, routes in `src/routes/`.
  Use a primitive that exists before writing markup that looks like one.
- Server state goes through TanStack Query hooks in the feature, never a
  `fetch` inside a component. Forms use React Hook Form with a Zod schema.
- Tests sit next to the code they test, or in the feature's `__tests__/`.
- Stack: TypeScript strict with `verbatimModuleSyntax`, React 19, React
  Router 7, Vite, Tailwind CSS v4, Radix with CVA, `lucide-react`, Recharts,
  Sonner, TanStack Query, React Hook Form and Zod. Do not add a second library
  for any of these jobs.

The UI learns which surfaces this release serves from `GET /api/features`
(`src/features/flags`). A surface that is off is hidden, not disabled.

## Design tokens

Percona brand: a purple accent on warm charcoal. The legacy orange is the
logo mark only, never a UI accent. Tokens are Tailwind v4 `@theme` values in
`src/index.css`, named by role, with light and dark defined together:

| Token                               | Light                                       | Dark                          |
| ----------------------------------- | ------------------------------------------- | ----------------------------- |
| `background`                        | white                                       | `#1d1c1c`, never pure black   |
| `foreground`                        | the `#282727` family                        | `#f5f4f5`, not pure white     |
| `card`                              | white                                       | `#262525`                     |
| `muted-foreground`                  | warm gray 500 (AA)                          | warm gray 400 (AA)            |
| `primary`                           | Percona purple `#653DF4` (AA, about 5.96:1) | the brighter `#b76cf5`        |
| `border`, `input`                   | warm gray 200                               | warm gray 800                 |
| `ring`                              | purple at 50%                               | purple at 50%                 |
| `success`, `warning`, `destructive` | green 600, amber 700, red 600               | green 400, amber 400, red 400 |

Components use the tokens only. `src/test/design-lock.test.ts` fails on a raw
palette class, a hex value, a radius other than `rounded-md`, `rounded-lg` or
`rounded-full`, text above `text-2xl`, serif type, an icon library other than
`lucide-react`, a `strokeWidth` other than 1.5, `dangerouslySetInnerHTML`, and
a skipped or focused test.

- Flat fills. No glows, neon gradients, glassmorphism or purple-tinted
  shadows. Shadows only on dialogs and popovers.
- Controls are `rounded-md`, cards, dialogs and panels `rounded-lg`, badges
  and status pills `rounded-full`.
- Layout: a fixed `w-64` sidebar, content in `max-w-6xl mx-auto px-6`,
  sections `py-8`, blocks `space-y-6`, columns in CSS Grid. Desktop first;
  tablet must not break; phone is best effort.

## Type and icons

- Poppins for text and JetBrains Mono for data, both self-hosted through
  `@fontsource`. Every number, connection string, hostname and log line is
  mono.
- Page title `text-2xl font-semibold tracking-tight`, section title
  `text-base font-semibold`, body `text-sm`, secondary
  `text-sm text-muted-foreground`, meta `text-xs text-muted-foreground`.
- Icons are `lucide-react` at `strokeWidth={1.5}`, `size-4` by default and
  `size-5` in empty states. Engine icons go through the engine icon component.
- Status is an icon plus text, never color alone.

## Components

- **Buttons.** `default` (purple) is the one primary action in a view;
  `outline` is secondary, `ghost` tertiary, `destructive` only for delete and
  only inside a confirmation dialog. The one exception is End access on a
  support access window, which cuts a support engineer off at once. Labels are verbs in sentence case, one to
  three words ("Create database", "Back up now"), and one intent has one label
  across the app. A loading button keeps its label ("Creating...") with a
  spinner.
- **Forms.** Label above the input, help text below the label, the error below
  the input. Never a placeholder as a label. Validate on blur, and keep submit
  disabled until the form is valid. The create form has one required field:
  the name.
- **Cards** only when elevation means something. `divide-y` for groups of
  rows, and never a card inside a card.
- **Tables** have at most five columns. An empty table shows the empty state,
  not bare headers.
- **Dialogs** are for destructive and data-changing actions only, plus the
  read-only connect dialog and the Experimental disclaimer. Delete asks for the database name and keeps
  confirm disabled until it matches. Restore creates a new database and leaves
  the source running; it does not replace data in place. There is no Blocked
  state. A dialog states its consequence in one plain sentence.
- **Toasts** (Sonner, bottom right, 4 seconds) carry transient confirmations.
  Never more than two at once, and never for something the user must not
  miss: that is a banner.
- **Status pills:** `Ready` (success), `Creating` (muted, soft pulse),
  `Degraded` (warning), `Failed` (destructive), `Metrics pending` (muted).

## Pages and states

- The sidebar starts with Environments, then Chats, AI agents, Activity and Support.
  Under the logo sits the organization switcher: a dialog for an admin or a
  person in several organizations, a static label for anybody else. An admin
  also gets an Organization group, which holds Teams, Users and Settings.
- Signed out, the card offers Google and GitHub first with the password form
  behind a disclosure, and signing up asks only for the organization name.
- The consent page (`/authorize-agent?request=<token>`) is a card outside the app shell. It names
  who is asking (the name flagged as the app's own claim, its address, where the answer goes, and a
  warning for a loopback address), pre-selects read and the person's own Team, and decides nothing on
  load. It follows the sign in `next` parameter only when it starts with `/oauth/authorize?`, and
  refuses to render inside a frame.
- The database detail tabs, in this order: Overview, Backups, Metrics,
  Activity, Alerts, History, Settings.
- A connection string shows its password masked to a fixed width, so the shape
  of the URI is visible and the credential is not. There is no reveal control:
  the clipboard is the only way to the real value, which keeps passwords out of
  screenshots and screen shares.
- **Loading:** skeletons shaped like the loaded layout. No full-page spinner
  after the first load.
- **Empty:** a composed empty state with one call to action.
- **Error:** inline where it happened: what happened, why when known, and one
  action (retry, open settings, or contact support).
- **Pending:** a "Metrics pending" badge where charts will be. Never a blank
  chart and never an error.

## Motion and accessibility

- Allowed motion: `animate-fade-in` on page entry, `animate-slide-up` for
  dialogs and toasts, the soft `Creating` pulse (the only perpetual animation),
  and a spinner that turns into a check. Animate `transform` and `opacity`
  only. No scroll effects, parallax, marquees, list layout animations or
  infinite shimmer. Everything is instant under `prefers-reduced-motion`.
- WCAG AA at least. Visible focus on every interactive element. A full
  keyboard path through create, scale, backup, restore and delete. Dialogs
  trap and return focus, and `Esc` closes the ones that are not destructive.
  Charts have a text equivalent.
- Dark mode is a token swap on `.dark` on `<html>`, shipped in the same PR as
  light.

## Tests

- Vitest and Testing Library. Render through `renderWithProviders` or
  `renderHookWithProviders` from `src/test/utils.tsx`, and build data with its
  `createMock*` factories. Never wire providers by hand in a test.
- Query by role and visible text, the way a user finds things. Use a test id
  only when there is no accessible handle.
- Playwright specs in `e2e/` stub the adapter at the network layer, so they
  need no backend. Most screens' specs write `test-results/<screen>-light.png`
  and `-dark.png`, and those files are the screenshots a UI pull request
  attaches. A screen you change without such a pair gets one in its spec.

## Review checklist

What no test catches. Walk it before opening a UI pull request:

- [ ] One primary action per view, and every button a short verb
- [ ] Every async action has loading, success and error states
- [ ] Every list has a composed empty state, and skeletons match the loaded shape
- [ ] Destructive actions sit in a dialog with typed confirmation
- [ ] Contrast passes in both themes, and focus rings are visible
- [ ] The keyboard path through the changed flow works
- [ ] No new motion beyond the allowed set, and reduced motion is instant
- [ ] Copy read out loud: no filler verbs, no invented numbers, no banned words
