export function parseSoraRawTokens(raw: string) {
  const tokens = String(raw || '')
    .split(/\r?\n/)
    .map((item) => item.trim())
    .filter(Boolean)

  const sessionTokens: string[] = []
  const accessTokens: string[] = []

  for (const token of tokens) {
    if (token.startsWith('eyJ')) {
      accessTokens.push(token)
    } else {
      sessionTokens.push(token)
    }
  }

  return { sessionTokens, accessTokens }
}
