import bcrypt from 'bcryptjs'

const AUTH_USER_RE = /^[A-Za-z0-9._-]{1,64}$/
const BCRYPT_COST = 10
const BCRYPT_MAX_PASSWORD_BYTES = 72

const utf8ByteLength = (value) => {
  let bytes = 0
  for (const char of value) {
    const codePoint = char.codePointAt(0)
    if (codePoint <= 0x7f) bytes += 1
    else if (codePoint <= 0x7ff) bytes += 2
    else if (codePoint <= 0xffff) bytes += 3
    else bytes += 4
  }
  return bytes
}

export const validateBasicAuthUsername = (username, password) => {
  if (!username && !password) return null
  if (!username) return 'Enter a username to enable basic auth'
  if (!AUTH_USER_RE.test(username)) {
    return "Use 1-64 letters, digits, '.', '_' or '-'"
  }
  return null
}

export const validateBasicAuthPassword = (username, password) => {
  if (!username && !password) return null
  if (!password) return 'Enter a password to enable basic auth'
  if (utf8ByteLength(password) > BCRYPT_MAX_PASSWORD_BYTES) {
    return 'Use at most 72 UTF-8 bytes; bcrypt would truncate a longer password'
  }
  return null
}

export const createHtpasswdEntry = async (username, password) => {
  const cleanUsername = username.trim()
  if (!cleanUsername && !password) return ''

  const error =
    validateBasicAuthUsername(cleanUsername, password) ||
    validateBasicAuthPassword(cleanUsername, password)
  if (error) throw new Error(error)

  const hash = await bcrypt.hash(password, BCRYPT_COST)
  return `${cleanUsername}:${hash}`
}
