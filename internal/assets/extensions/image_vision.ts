// image_vision.ts — 多模态 AI 图片识别插件（内置通用版）
/**
 * 用多模态 AI 模型识别图片内容（支持截图/照片/图表/UI 界面）。
 * 何时用：用户给出图片路径并要求描述/分析图片内容时，这是首选工具。
 * 参数: {path: string} 图片绝对路径（支持 jpg/png/gif/webp/bmp）；{prompt?: string} 可选自定义提示词。
 *
 * 配置来源：~/.mizar/config.json 的 base_url / api_key / model（与主程序一致）。
 */
export function tool_image_describe(args: string): string {
  const p = JSON.parse(args) as { path: string; prompt?: string };
  if (!p.path) return '错误: 需要指定图片路径';

  const imgStr = fs_read(p.path);
  if (!imgStr) return `错误: 无法读取图片 ${p.path}`;

  const b64 = base64_encode(imgStr);
  const mimeType = detectMimeType(p.path);
  const config = loadConfig();
  const userPrompt = p.prompt || '请详细描述这张图片的内容。';

  const body = JSON.stringify({
    model: config.model,
    messages: [{
      role: 'user',
      content: [
        { type: 'text', text: userPrompt },
        { type: 'image_url', image_url: { url: `data:${mimeType};base64,${b64}` } }
      ]
    }]
  });

  // http_post 不支持自定义 headers；带鉴权头的请求必须用 http_request(method, url, body, headersJSON)
  const headers = JSON.stringify({
    'Authorization': 'Bearer ' + config.apiKey,
    'Content-Type': 'application/json'
  });
  const resp = http_request('POST', config.baseUrl + '/chat/completions', body, headers);

  try {
    const result = JSON.parse(resp);
    const content = result.choices?.[0]?.message?.content;
    if (!content) return 'AI 返回无内容: ' + resp.slice(0, 300);
    return content;
  } catch {
    return `AI 调用失败: ${resp.slice(0, 200)}`;
  }
}

interface Config { baseUrl: string; apiKey: string; model: string }

function loadConfig(): Config {
  // 优先环境变量（env 宿主函数），其次 ~/.mizar/config.json
  const getEnv = (k: string): string => { try { return env(k) || ''; } catch (e) { return ''; } };
  let baseUrl = getEnv('MIZAR_BASE_URL');
  let apiKey = getEnv('MIZAR_API_KEY');
  let model = getEnv('MIZAR_MODEL');
  try {
    const home = getEnv('MIZAR_HOME') || getEnv('HOME') || '/root';
    const raw = fs_read(home + '/.mizar/config.json');
    if (raw) {
      const cfg = JSON.parse(raw);
      baseUrl = baseUrl || cfg.base_url || '';
      apiKey = apiKey || cfg.api_key || '';
      model = model || cfg.model || '';
    }
  } catch {}
  if (!baseUrl) throw new Error('未配置 base_url（~/.mizar/config.json 或 MIZAR_BASE_URL）');
  if (!model) model = 'gpt-4o';
  return { baseUrl, apiKey, model };
}

function detectMimeType(path: string): string {
  const ext = path.split('.').pop()?.toLowerCase();
  switch (ext) {
    case 'png': return 'image/png';
    case 'gif': return 'image/gif';
    case 'webp': return 'image/webp';
    case 'bmp': return 'image/bmp';
    default: return 'image/jpeg';
  }
}
