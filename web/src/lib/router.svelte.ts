export type Route =
  | { name: 'home' }
  | { name: 'room'; id: string }
  | { name: 'player'; id: string }
  | { name: 'admin' }
  | { name: 'tv' }

function match(path: string): Route {
  let m = path.match(/^\/r\/([A-Za-z0-9_-]+)\/player\/?$/)
  if (m) return { name: 'player', id: m[1] }
  m = path.match(/^\/r\/([A-Za-z0-9_-]+)\/?$/)
  if (m) return { name: 'room', id: m[1] }
  if (/^\/admin\/?$/.test(path)) return { name: 'admin' }
  if (/^\/tv\/?$/.test(path)) return { name: 'tv' }
  return { name: 'home' }
}

let path = $state(location.pathname)
addEventListener('popstate', () => (path = location.pathname))

export const router = {
  get route(): Route {
    return match(path)
  },
  navigate(to: string) {
    history.pushState(null, '', to)
    path = location.pathname
  },
}
