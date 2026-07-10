import bcrypt from 'bcryptjs'
import {
  createHtpasswdEntry,
  validateBasicAuthPassword,
  validateBasicAuthUsername
} from './basicAuth'

test('leaves basic auth disabled when both fields are empty', async () => {
  await expect(createHtpasswdEntry('', '')).resolves.toBe('')
})

test('requires a complete username and password pair', () => {
  expect(validateBasicAuthUsername('', 'secret')).toMatch(/username/)
  expect(validateBasicAuthPassword('alice', '')).toMatch(/password/)
})

test('mirrors the backend username rules', () => {
  expect(validateBasicAuthUsername('alice.smith-1', 'secret')).toBeNull()
  expect(validateBasicAuthUsername('alice:admin', 'secret')).not.toBeNull()
  expect(validateBasicAuthUsername('a'.repeat(65), 'secret')).not.toBeNull()
})

test('rejects passwords that bcrypt would truncate', () => {
  expect(validateBasicAuthPassword('alice', 'a'.repeat(72))).toBeNull()
  expect(validateBasicAuthPassword('alice', 'a'.repeat(73))).not.toBeNull()
  expect(validateBasicAuthPassword('alice', '🔐'.repeat(18))).toBeNull()
  expect(validateBasicAuthPassword('alice', '🔐'.repeat(19))).not.toBeNull()
})

test('creates an htpasswd entry that verifies with the entered password', async () => {
  const entry = await createHtpasswdEntry(' alice ', 'correct horse battery staple')
  const [username, hash] = entry.split(':')

  expect(username).toBe('alice')
  expect(hash).toMatch(/^\$2[aby]\$10\$/)
  await expect(bcrypt.compare('correct horse battery staple', hash)).resolves.toBe(
    true
  )
  await expect(bcrypt.compare('wrong password', hash)).resolves.toBe(false)
})
