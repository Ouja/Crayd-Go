# Crayd.go

A Go version of [Crayd](https://github.com/luminairre/Crayd2), the tiny PHP MVC framework created by [luminairre](https://github.com/luminairre).

**This is a prototype**, built out of nostalgia for the old micro framework: the same
`/controller/action/key/value` URLs, the same `controller_action` view names, the same `layout.html`,
the same Pre/PostDispatch hooks, but with safe defaults (escaped output, CSRF protection, hashed passwords,
parameterised SQL) and nothing but the Go standard library in the core. It works and is tested, but it has had
no real-world use. Treat it as something to learn from and play with before you bet a product on it.

## Run

    cp .env.example .env    # optional: your local settings
    go run .                # then open http://localhost:8080
    go test ./...

The route table is printed at startup.

## Usage guide

Start here if you are new. The rest of this file is reference. The guide builds one small feature, a notes page
(`/note`), and touches every layer on the way.

### 1. Quick start

You need Go 1.24 or newer. Nothing else: no database, no Redis.

    cp .env.example .env     # optional
    go run .                 # http://localhost:8080
    go test ./...

The route table is printed at startup, so you always see what URLs exist. In development, edits to
`app/views/` show up on refresh. Go code needs a restart.

### 2. Your first page (no code)

Create `app/views/hello_index.html`:

    <p>Hello from a view-only page.</p>

Open `/hello`. A view with no controller is served as is, inside `layout.html`. The same rule gave
`/about` its page.

### 3. Your first controller

A controller is a struct that embeds `Base` (which embeds `crayd.Controller`). Each exported method with no
arguments is an action, and its URL comes from the names. `Hello` in controller `index` becomes `/index/hello`,
and since `index` is the fallback controller, also `/hello`.

    // controllers/greet.go
    type Greet struct{ Base }

    // GET /greet/hi/name/Ouja
    func (c *Greet) Hi() { c.View["name"] = c.Param("name", "stranger") }

Register it in `buildApp` (in `main.go`) and add the view `app/views/greet_hi.html`:

    app.Register("greet", &controllers.Greet{})

    <p>Hi {{ .name }}!</p>

Everything in `c.View` is available in the template as `.name`. Output is HTML-escaped, so a visitor
named `<script>` is shown as text.

What you can do inside an action:

| Want | Write |
|---|---|
| read input | `c.Param("k", "default")`, `c.ParamInt("id", 0)` (query, `/key/value` pairs and form fields, later ones win) |
| show a different view | `c.SetView("note_new")` |
| set the status | `c.SetStatus(422)` |
| redirect | `c.Redirect("/note")` |
| JSON instead of HTML | `c.JSON(200, map[string]any{"ok": true})` |
| 404 | `c.NotFound()` |
| code before/after every action | `PreDispatch()` / `PostDispatch()` on the controller (`Base` already has one) |

Which HTTP method? An action named `Add` answers GET. `PostAdd` answers POST (also `GetX`, `PutX`, `PatchX`,
`DeleteX`). The wrong method gets a 405.

### 4. Put the logic in a model

Keep controllers thin. Rules, validation and anything you would want to test without a web server go in `models/`.
The model talks to storage through a small interface that you define, so it knows nothing about SQL.

    // models/note.go
    type Note struct {
        ID        int64
        Body      string
        CreatedAt time.Time
    }

    type NoteStore interface {
        Add(ctx context.Context, n *Note) error // sets n.ID
        All(ctx context.Context) ([]Note, error)
    }

    type NoteService struct{ Store NoteStore }

    func (s *NoteService) Add(ctx context.Context, body string) (*Note, error) {
        body = strings.TrimSpace(body)
        if body == "" {
            return nil, ValidationError{"body": "Write something first"}
        }
        n := &Note{Body: body, CreatedAt: time.Now()}
        if err := s.Store.Add(ctx, n); err != nil {
            return nil, err
        }
        return n, nil
    }

    func (s *NoteService) List(ctx context.Context) ([]Note, error) { return s.Store.All(ctx) }

`ValidationError` is a `map[string]string` of field to message, so the controller can hand it straight to the form.

### 5. The controller and views for a form

    // controllers/note.go
    type Note struct {
        Base
        Notes *models.NoteService      // injected in main.go, see step 6
    }

    // GET /note
    func (c *Note) Index() {
        list, err := c.Notes.List(c.Ctx())
        if err != nil {
            panic(err) // recovered: the visitor sees a 500, the log has the details
        }
        c.View["notes"] = list
    }

    // GET /note/new
    func (c *Note) New() {}

    // POST /note/add
    func (c *Note) PostAdd() {
        body := c.Param("body", "")
        _, err := c.Notes.Add(c.Ctx(), body)
        var verr models.ValidationError
        if errors.As(err, &verr) {              // bad input: show the form again
            c.View["errors"] = map[string]string(verr)
            c.View["form"] = map[string]string{"body": body}
            c.SetView("note_new")
            c.SetStatus(422)
            return
        }
        if err != nil {
            panic(err)
        }
        c.Flash("Note saved")                   // shown once on the next page
        c.Redirect("/note")                     // after a POST always redirect
    }

`app/views/note_index.html`:

    <h2>Notes</h2>
    <p><a href="/note/new">Add a note</a></p>
    <ul>
    {{ range .notes }}<li>{{ .Body }} <small>{{ .CreatedAt.Format "2 Jan 15:04" }}</small></li>
    {{ else }}<li>Nothing yet.</li>
    {{ end }}
    </ul>

`app/views/note_new.html`:

    <h2>New note</h2>
    <form method="post" action="/note/add">
      {{ $.csrf.Field }}
      <p>
        <textarea name="body">{{ index .form "body" }}</textarea>
        {{ with .errors.body }}<small class="error">{{ . }}</small>{{ end }}
      </p>
      <button>Save</button>
    </form>

Two things to remember about forms:
- **Every form needs `{{ $.csrf.Field }}`.** Without it the POST is refused with 403. That is the CSRF protection doing its job.
  (`$` is needed so it also works inside `range` and `with`.)
- `Base.PreDispatch` sets empty `errors` and `form` maps, so `.errors.body` and `index .form "body"` never fail on a first visit.

### 6. Store and wiring

First an in-memory store, enough to run and to test:

    // store/memory_note.go
    type MemoryNotes struct {
        mu   sync.Mutex
        list []models.Note
    }

    func NewMemoryNotes() *MemoryNotes { return &MemoryNotes{} }

    func (s *MemoryNotes) Add(_ context.Context, n *models.Note) error {
        s.mu.Lock()
        defer s.mu.Unlock()
        n.ID = int64(len(s.list) + 1)
        s.list = append([]models.Note{*n}, s.list...) // newest first
        return nil
    }

    func (s *MemoryNotes) All(context.Context) ([]models.Note, error) {
        s.mu.Lock()
        defer s.mu.Unlock()
        return append([]models.Note(nil), s.list...), nil
    }

Then wire it in `buildApp`. Services are created once here and handed to the controller:

    notes := &models.NoteService{Store: store.NewMemoryNotes()}
    app.Register("note", &controllers.Note{Notes: notes})

Run it, open `/note`, add a note. Restarting loses the notes, which is what the next step fixes.

### 7. A real database (MySQL / MariaDB)

Add a migration, `migrations/003_create_notes.sql`:

    CREATE TABLE notes (
      id         BIGINT AUTO_INCREMENT PRIMARY KEY,
      body       VARCHAR(2000) NOT NULL,
      created_at DATETIME(6)   NOT NULL
    ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

and a store, `store/mysql_note.go`:

    type NoteSQL struct{ DB *db.DB }

    const insertNote db.Query = "INSERT INTO notes (body, created_at) VALUES (?, ?)"

    func (s *NoteSQL) Add(ctx context.Context, n *models.Note) error {
        id, err := s.DB.Insert(ctx, insertNote, n.Body, n.CreatedAt.UTC())
        if err != nil {
            return err
        }
        n.ID = id
        return nil
    }

    func (s *NoteSQL) All(ctx context.Context) ([]models.Note, error) {
        var list []models.Note
        err := s.DB.Select(ctx, &list, "SELECT * FROM notes ORDER BY id DESC")
        return list, err
    }

In `run()` in `main.go`, next to where `NewUserSQL` and `NewOrderSQL` are chosen, use `&store.NoteSQL{DB: conn}`
instead of `store.NewMemoryNotes()` when `DB_DSN` is set (add a `Notes models.NoteStore` field to `Deps`, like `Users`).
Then:

    DB_DSN=user:pass@tcp(localhost:3306)/crayd go run .     # applies migration 003 on start (development)

Rules of the database helper, in short:
- Write `?` for every value. SQL that is built by gluing strings (`"... WHERE x = '" + x + "'"`) does not compile.
- Columns fill struct fields by snake_case name (`CreatedAt` is `created_at`) or a `db:"name"` tag.
- Several steps that must succeed together go in `DB.Tx(ctx, func(tx *db.Tx) error { ... })`. Return an error to roll back.
  `OrderSQL.Create` in `store/mysql_order.go` is the example.
- Migrations only go forward. To change a table, add `004_...sql`. Never edit one that has been applied.
- On PostgreSQL, `INSERT` needs `... RETURNING id` and the DDL differs (`BIGSERIAL`, `TIMESTAMP`).

### 8. Protect pages with a login

Registration, login and logout already exist (`/auth/register`, `/auth/login`). To require a login, add this to the controller:

    func (c *Note) PreDispatch() {
        c.Base.PreDispatch()
        c.RequireLogin("/auth/login")
    }

`RequireLogin` redirects anonymous visitors to the login page with `?next=` so they come back afterwards. Once a
hook has redirected or rendered, the framework skips the action. `c.UserID()` is the signed-in user's ID (a string, empty when anonymous), so a note
can store who wrote it. Always check ownership in the model, not in the template: when you load a note by ID, also
require that it belongs to `c.UserID()`.

### 9. Tests

Models are plain Go, so test them with the in-memory store and no server:

    func TestNoteRejectsBlank(t *testing.T) {
        svc := &models.NoteService{Store: store.NewMemoryNotes()}
        if _, err := svc.Add(context.Background(), "  "); err == nil {
            t.Fatal("blank note accepted")
        }
    }

To test a whole page, `main_test.go` has a small browser-like client with a cookie jar that fetches the CSRF token
from a form, then posts it, just like a browser. `ada.post("/note/new", "/note/add", url.Values{"body": {"hi"}})`
and `expect(t, resp, 303)` are all you need. Run everything with the race detector: `go test -race ./...`.

### 10. Deploying

1. Build: `go build -o crayd-go .` (one binary; views are read from `app/views/` and static files from `public/`, so ship those folders next to it. Migrations are inside the binary).
2. Set the environment, for example:

       APP_ENV=production
       ADDR=:8080
       DB_DSN=user:pass@tcp(db:3306)/crayd
       REDIS_ADDR=redis:6379          # or SESSION_SECRET=<32+ random characters>

   Production refuses to start when `DB_DSN`, or a session store (`REDIS_ADDR` or `SESSION_SECRET`), is missing.
   All problems are listed at once.
3. Apply migrations as a deploy step: `./crayd-go migrate`.
4. Put it behind a TLS-terminating proxy (nginx, Caddy, your cloud load balancer). Session cookies are `Secure`, so
   they only work over HTTPS. `http://localhost` is the one exception browsers allow.
5. Stop it with SIGTERM or Ctrl+C. Running requests are allowed to finish (`SHUTDOWN_TIMEOUT`, default 15s).

Not included: login rate limiting, email sending, password reset, user roles.

## Project layout

    crayd/         the framework (standard library only)
    controllers/   thin: read params, call a model, pick a view
    models/        business rules, plain Go (no HTTP, SQL or Redis); defines the Cache and store interfaces
    store/         implementations: in-memory, Redis (cache, sessions), MySQL/MariaDB (users, orders)
    migrations/    numbered .sql files, embedded in the binary
    app/views/     templates named <controller>_<action>.html, plus layout.html
    public/        static files
    main.go        wiring: create services once, inject them, register controllers

A function that needs `c.Param` or `c.W` belongs in a controller. If it could run from a test
with no web server, it belongs in `models`.

## Conventions

| URL | Goes to |
|---|---|
| `/` | `Index.Index()` |
| `/hello` | `Index.Hello()` (unknown first segment falls back to the index controller) |
| `/user/profile/id/5/tab/posts` | `User.Profile()`, `Param("id") == "5"` |
| `/user/id/7` | `User.Index()` with `id=7` (unknown action falls back to index) |
| `/user/show-all` | `User.ShowAll()` (CamelCase becomes kebab-case) |
| `POST /hello` | `Index.PostHello()` (verb prefixes: Get, Post, Put, Patch, Delete) |
| `/order/show/id/5` | `Order.Show()` |
| `/about` | `app/views/about_index.html`, no controller needed |
| `/css/app.css` | `public/css/app.css` (static files win over routes) |

- **Controller**: a struct embedding `crayd.Controller`, registered with `app.Register("name", &Struct{...})`.
- **Action**: an exported method with no arguments and no results. Without a verb prefix it answers GET/HEAD.
  A wrong verb gets `405 Method Not Allowed`.
- **View**: `app/views/<controller>_<action>.html`, wrapped by `app/views/layout.html` via `{{ template "content" . }}`.
  Output is HTML-escaped automatically.
- **Hooks**: optional `PreDispatch()` / `PostDispatch()` methods.
- **Params**: query < `/key/value` URL pairs < form body. `Param`, `ParamInt`, `ParamInt64`, `Params`.
- **Responses**: render the view (default), `SetView`, `SetStatus`, `Redirect`, `JSON`, `NotFound`.

## Dependency injection

Create services once in `main.go` and set them on the struct you register:

    app.Register("order", &controllers.Order{Orders: orderService})

That struct is a prototype: every request gets a fresh copy, so services are shared
while `View` and params stay per request.

## Example: orders (`/order`)

`controllers/order.go` -> `models/order.go` (validation, 10% discount from $100, cache-aside reads)
-> `store/` (memory store, or MySQL/MariaDB when `DB_DSN` is set).

    app.Register("order", &controllers.Order{Orders: orders})

The model depends on small interfaces (`models.OrderStore`, `models.Cache`), so tests use the
in-memory versions and production can use Redis:

    REDIS_ADDR=localhost:6379 go run .     # cache + sessions in Redis
    go run .                               # everything in memory

Redis is optional. The Redis driver is imported only by `store/redis_cache.go`.
To run the Redis test against a real server: `REDIS_TEST_ADDR=localhost:6379 go test ./store`.

## Sessions, login and CSRF

    app.Sessions(crayd.SessionOptions{Store: crayd.ServerSessions(store.NewRedisSessions(rdb))})

| Store | Where data lives | Logout really revokes? |
|---|---|---|
| `crayd.ServerSessions(backend)` | Redis (`store.NewRedisSessions`) or memory (`crayd.NewMemorySessionBackend`) | yes |
| `crayd.CookieSessions(secret)` | in the cookie, AES-256-GCM encrypted (secret >= 32 bytes) | no: a copied cookie works until it expires |

Sessions load lazily (pages that don't use them set no cookie). Cookies are `HttpOnly`, `Secure`,
`SameSite=Lax`, with a 12 hour sliding idle timeout. Server stores keep only a SHA-256 of the session ID.

In controllers:

    c.Session().Get("k") / Set / Delete
    c.Flash("Saved")                  // shown once as {{ .flash }}
    c.LoginAs(userID)                 // renews session ID and CSRF token (stops session fixation)
    c.LogoutUser()
    c.UserID()                        // "" when anonymous
    c.RequireLogin("/auth/login")     // in PreDispatch; adds ?next= for GET requests
    crayd.SafeRedirect(next, "/")     // blocks open redirects like //evil.com

**CSRF** is on for every POST/PUT/PATCH/DELETE, in two layers:
1. Browser origin check (`Sec-Fetch-Site` / `Origin`); cross-origin requests get 403. Allow extra origins with `app.TrustOrigins("https://admin.example.com")`.
2. A per-session token. Put `{{ $.csrf.Field }}` inside every form (the `$` makes it work inside `range`/`with`).
   For JavaScript, send `{{ $.csrf.Token }}` in an `X-CSRF-Token` header.

`app.SkipCSRF("api")` turns both layers off for a controller that doesn't use cookies (bearer-token APIs, webhooks).

**Passwords**: `crayd/password` (no HTTP code, safe to use from models) uses PBKDF2-SHA256 with 600,000
iterations from Go's standard library. `password.Check("", pw)` for unknown users keeps timing equal.
Hashes record their parameters; `NeedsRehash` lets you upgrade them on login (the example does).
If you prefer bcrypt or argon2id, swap the package; the hash format leaves room for that.

Not included yet: login rate limiting (put a middleware or Redis counter in front of `/auth/login`).

## Configuration, development mode and shutdown

Settings come from environment variables. `crayd.LoadDotEnv(".env")` also reads a `.env` file
(copy `.env.example`; `.env` is git-ignored). Real environment variables always win over the file.
Wrong or missing settings stop the app at startup with every problem listed at once.

| Variable | Meaning | Default |
|---|---|---|
| `APP_ENV` | `development` or `production` | `development` |
| `ADDR` / `PORT` | listen address / port | `:8080` |
| `REDIS_ADDR` | cache and sessions in Redis | in memory |
| `SESSION_SECRET` | 32+ characters: encrypted-cookie sessions without Redis | none |
| `INSECURE_COOKIES` | `1` allows session cookies over plain HTTP (LAN testing only) | off |
| `SHUTDOWN_TIMEOUT` | how long running requests may take to finish on stop | `15s` |

`production` refuses to start without `REDIS_ADDR` or `SESSION_SECRET` (otherwise everyone is signed out
on every restart) and refuses `INSECURE_COOKIES`.

In your own code use `crayd.Env` for typed settings: `e := &crayd.Env{}`, then `e.String`, `e.Required`,
`e.Int`, `e.Bool`, `e.Duration`, `e.OneOf`, and `e.Err()` once at the end.
In `.env`, write Windows paths unquoted or in single quotes (inside double quotes `\t` and `\n` are escapes).

**Development mode** (`app.Dev(true)`, on when `APP_ENV=development`): edit anything in `app/views/`,
refresh the browser, done. New and deleted view files count too. A template error is shown in the
browser instead of a blank page, and clears when you fix the file. Go code still needs a restart.
In production templates are parsed once at startup and errors never reach the browser.

**Graceful shutdown**: `app.Run` stops on Ctrl+C or SIGTERM. It stops accepting connections, lets running
requests finish (up to `ShutdownTimeout`, then closes them), and then runs the cleanup you registered:

    app.OnShutdown(func(ctx context.Context) error { return rdb.Close() })   // last registered runs first

A second Ctrl+C quits immediately. Running requests keep a live `c.Ctx()`, so a database query in progress
isn't cancelled by the shutdown. `app.RunContext(ctx, addr)` and `app.Serve(ctx, listener)` are there
for tests and custom setups.

## Database (`crayd/db`)

A small helper over `database/sql`. No driver inside: import the one you use
(`_ "github.com/go-sql-driver/mysql"`, `_ "github.com/jackc/pgx/v5/stdlib"` or `_ "github.com/lib/pq"`).

    conn, err := db.Open(ctx, "mysql", os.Getenv("DB_DSN"))   // or "pgx" / "postgres"

    var o Order
    err = conn.Get(ctx, &o, "SELECT * FROM orders WHERE id = ?", id)          // db.ErrNotFound if no row
    var list []Order
    err = conn.Select(ctx, &list, "SELECT * FROM orders WHERE id IN (?)", ids) // slice -> ?, ?, ?
    n, err := conn.Exec(ctx, "UPDATE orders SET paid = ? WHERE id = ?", true, id)
    id, err := conn.Insert(ctx, "INSERT INTO orders (customer) VALUES (?) RETURNING id", name) // RETURNING only on PostgreSQL
    err = conn.Tx(ctx, func(tx *db.Tx) error { ... })  // commit on nil, rollback on error or panic

- Always write `?`; PostgreSQL gets `$1, $2, ...`. `??` is a literal `?` (jsonb operators). A `?` inside quotes or comments is left alone.
- **Queries must be constants.** `conn.Get(ctx, &o, "... WHERE email = '" + email + "'")` does not compile. For SQL you
  must assemble at runtime from trusted pieces (whitelisted column names), use `db.Dynamic(...)`, which is easy to find in review.
- Columns map to fields by `db:"name"` tag or snake_case (`UserID` -> `user_id`); embedded structs are flattened.
  A column with no matching field is an error, so typos don't silently become empty values.
- `NULL` columns: use pointer fields (`*string`) or `sql.NullString`. For MySQL `DATETIME` into `time.Time`, add `parseTime=true` to the DSN.

## MySQL / MariaDB stores and migrations

With `DB_DSN` set, users and orders live in MySQL/MariaDB (`store/mysql*.go`); without it they live in memory.

    DB_DSN=user:pass@tcp(localhost:3306)/crayd
    AUTO_MIGRATE=1          # default in development; in production run `go run . migrate` as a deploy step

- Schema changes are plain files in `migrations/` (`003_add_something.sql`), applied in name order, each once,
  recorded in `schema_migrations`. A lock stops two instances migrating at the same time. MySQL cannot roll back DDL,
  so keep one change per file.
- The DSN is normalised for you: `parseTime=true`, UTC, `clientFoundRows=true`, `multiStatements=false`. An invalid DSN error never prints the password.
- Production requires `DB_DSN`.
- The MySQL driver is pinned to v1.7.1 (newer ones need hosts blocked while this was written); `go get -u github.com/go-sql-driver/mysql` is fine.

Testing against a real server (use an empty database whose name ends in `_test`; its tables are dropped):

    MYSQL_TEST_DSN="user:pass@tcp(localhost:3306)/crayd_test" go test ./store -run MySQL -v

Status: the SQL and DDL were checked on PostgreSQL 16 and with a fake driver, but not on a real MySQL.
Run the command above once before relying on it.

## Middleware

Any standard `func(http.Handler) http.Handler` works. The first one added runs first.

    app.Use(crayd.Logger, myCORS)

Panics in actions or middleware are recovered and logged; the user gets a 500.

## Status

- [x] Milestone 1: router, DI, middleware, panic recovery, verb actions, JSON, static files, route table, tests
- [x] Milestone 2: controllers / models / store layout, order example, Redis + in-memory cache, tests
- [x] Milestone 3: sessions (Redis/memory/encrypted cookie), login helpers, CSRF, passwords, example login/register
- [x] Milestone 4: `crayd/db` helper, config + `.env`, dev template reload, graceful shutdown, MySQL/MariaDB stores + migrations
- [x] Usage guide (quick start to deployment)
- [x] Layouting System for View
