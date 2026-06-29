import { createFileRoute } from '@tanstack/react-router'

export const Route = createFileRoute('/')({
  component: Home,
})

function Home() {
  return (
    <main className="placeholder">
      <section className="status-panel" aria-labelledby="placeholder-title">
        <p className="eyebrow">Local web server</p>
        <h1 id="placeholder-title">flowstate</h1>
        <p className="lede">
          The embedded web shell is installed. Flow management remains in the terminal UI while
          the local server grows into this surface.
        </p>
        <dl className="facts" aria-label="Placeholder status">
          <div>
            <dt>Shell</dt>
            <dd>Static SPA</dd>
          </div>
          <div>
            <dt>API</dt>
            <dd>Protected GraphQL endpoint</dd>
          </div>
          <div>
            <dt>Controls</dt>
            <dd>Not implemented in this placeholder</dd>
          </div>
        </dl>
      </section>
    </main>
  )
}
