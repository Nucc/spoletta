// The models an agent process picks from (ModelPicker.tsx): a model's short
// name, without its provider and the path some providers put before it, and
// whether it is one for something else than an agent's conversation, such
// as speech, images, embeddings or moderation, or one of the old completion
// models and their fine-tunes, which the picker lists apart. Only its ID
// says, so it is told by the words in it; one that is unclear counts as a
// chat model, so none an agent could use is out of the way. scripts/models.ts
// checks it.

// A model as provider/id, such as fireworks/accounts/fireworks/models/kimi-k3,
// by the last part of its id: kimi-k3.
export function shortModel(model: string): string {
  const id = model.slice(model.indexOf('/') + 1)
  return id.slice(id.lastIndexOf('/') + 1) || id
}

// Words a model's name holds, whole between dashes, dots, colons or digits,
// when it is for speech, images, embeddings, moderation or the like.
const OTHER_WORDS = /(^|[-_.:])(tts|transcribe|whisper|audio|realtime|live|image|dall-e-?\d*|sora|embed|embedding|rerank|reranker|moderation|instruct|computer-use)(?=$|[-_.:\d])/

// The completion models before chat, and their fine-tunes.
const LEGACY = /^(ada|babbage|curie|davinci)(-\d+)?(:ft-.*)?$/

export function otherModel(model: string): boolean {
  const name = shortModel(model).toLowerCase()
  return LEGACY.test(name) || OTHER_WORDS.test(name)
}
