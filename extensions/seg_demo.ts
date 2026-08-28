// seg_demo.ts — 中文分词宿主函数使用示例
// 加载后 (/reload) 在 Agent 中可直接调用 tool_seg_demo

export function tool_seg_demo(args: string): string {
  const p = JSON.parse(args);
  const text = p.text || "我爱北京天安门，今天天气真好。Hello world!";

  // 调用宿主函数 seg_cut 做纯分词
  const words = seg_cut(text);
  
  // 调用宿主函数 seg_pos 做带词性标注的分词
  const posJSON = seg_pos(text);
  const posTokens = JSON.parse(posJSON);

  // 按词性过滤：只保留名词 (n, ns, nz 等) 和动词 (v)
  const nouns = posTokens.filter((t: any) => t.pos.startsWith("n"));
  const verbs = posTokens.filter((t: any) => t.pos.startsWith("v"));
  const adjectives = posTokens.filter((t: any) => t.pos.startsWith("a"));

  return JSON.stringify({
    text: text,
    words: words,
    posTokens: posTokens,
    filtered: {
      nouns: nouns.map((t: any) => t.text),
      verbs: verbs.map((t: any) => t.text),
      adjectives: adjectives.map((t: any) => t.text),
    },
    // 按名词+动词+形容词提取关键词（可用于记忆检索）
    keywords: [
      ...nouns.map((t: any) => t.text),
      ...verbs.map((t: any) => t.text),
      ...adjectives.map((t: any) => t.text),
    ],
  }, null, 2);
}