// Shared log line parser for console, header log strip, and dashboard hero panels.
// Splits each line into styled segments: dimmed prefix (timestamps, thread names),
// colored log level keyword, and classified message content.
// Handles formats from Minecraft, Source, Rust, Valheim, 7D2D, ARK, and more.

export type LogSeg = { text: string; cls: string };

export function parseLine(line: string): LogSeg[] {
  // Stack traces — entire line
  if (/^\s+(at |\.{3} \d+ more)/.test(line)) return [{ text: line, cls: 'log-trace' }];
  if (/^Caused by:/.test(line)) return [{ text: line, cls: 'log-msg-error' }];

  // Parse prefix: consume timestamps, brackets, and log levels from the front
  let i = 0;
  let prefixEnd = 0;

  while (i < line.length) {
    const rest = line.slice(i);

    // Skip separators between prefix parts (spaces, colons, dashes, >)
    const sepMatch = rest.match(/^[\s:\->]+/);
    if (sepMatch && prefixEnd > 0) { i += sepMatch[0].length; continue; }

    // [bracketed group] — timestamps, thread names, log sources
    if (rest[0] === '[') {
      const close = line.indexOf(']', i + 1);
      if (close !== -1) { i = close + 1; prefixEnd = i; continue; }
    }

    // Source engine "L " at line start
    if (i === 0 && rest.startsWith('L ')) { i += 2; continue; }

    // Timestamps in various formats
    let ate = false;
    const tsPatterns = [
      /^\d{4}[.\-/]\d{2}[.\-/]\d{2}[\sT.\-]+\d{2}[.:]\d{2}[.:]\d{2}[.\d:]*/,  // 2024-01-15T12:34:56.789
      /^\d{2}[/]\d{2}[/]\d{4}[\s\-]+\d{2}:\d{2}:\d{2}[.\d:]*/,                  // 01/15/2024 - 12:34:56
      /^\d{2}:\d{2}:\d{2}[.\d]*/,                                                  // 12:34:56.789
    ];
    for (const p of tsPatterns) {
      const m = rest.match(p);
      if (m) { i += m[0].length; prefixEnd = i; ate = true; break; }
    }
    if (ate) continue;

    // Elapsed time float after timestamp (7D2D: "1.234")
    const floatM = rest.match(/^\d+\.\d+(?=\s)/);
    if (floatM && prefixEnd > 0) { i += floatM[0].length; prefixEnd = i; continue; }

    // Bare log level keyword (only if we've already seen some prefix)
    const lvlM = rest.match(/^(INFO|INF|WARN|WARNING|WRN|ERROR|ERR|FATAL|SEVERE|DEBUG|DBG|TRACE)\b/i);
    if (lvlM && prefixEnd > 0) { i += lvlM[0].length; prefixEnd = i; continue; }

    break;
  }

  // Consume trailing separators after prefix
  while (prefixEnd < line.length && /[\s:\->]/.test(line[prefixEnd])) prefixEnd++;

  const prefix = line.slice(0, prefixEnd);
  const message = line.slice(prefixEnd);

  // No prefix detected — classify as a whole line
  if (!prefix) return [{ text: line, cls: classifyMessage(line, null) }];

  // Find level keyword in prefix for coloring
  const lvlMatch = prefix.match(/\b(INFO|INF|WARN|WARNING|WRN|ERROR|ERR|FATAL|SEVERE|DEBUG|DBG|TRACE)\b/i);
  const level = lvlMatch ? normalizeLevel(lvlMatch[1]) : null;

  const segs: LogSeg[] = [];

  // Split prefix around the level keyword so it gets its own color
  if (lvlMatch) {
    const idx = prefix.indexOf(lvlMatch[0]);
    if (idx > 0) segs.push({ text: prefix.slice(0, idx), cls: 'log-dim' });
    segs.push({ text: lvlMatch[0], cls: levelCls(level!) });
    const after = prefix.slice(idx + lvlMatch[0].length);
    if (after) segs.push({ text: after, cls: 'log-dim' });
  } else {
    segs.push({ text: prefix, cls: 'log-dim' });
  }

  if (message) segs.push({ text: message, cls: classifyMessage(message, level) });

  return segs;
}

function normalizeLevel(s: string): string {
  const u = s.toUpperCase();
  if (u === 'INF') return 'INFO';
  if (u === 'WRN' || u === 'WARNING') return 'WARN';
  if (u === 'ERR') return 'ERROR';
  if (u === 'DBG') return 'DEBUG';
  return u;
}

function levelCls(level: string): string {
  switch (level) {
    case 'ERROR': case 'FATAL': case 'SEVERE': return 'log-lvl-error';
    case 'WARN': return 'log-lvl-warn';
    case 'DEBUG': case 'TRACE': return 'log-lvl-debug';
    default: return 'log-lvl-info';
  }
}

function classifyMessage(msg: string, level: string | null): string {
  const lower = msg.toLowerCase();

  // Chat: <PlayerName> message
  if (/^<\w+>/.test(msg)) return 'log-chat';

  // Player join
  if (lower.includes('joined the game') || lower.includes('logged in') ||
      lower.includes(' connected') || lower.includes('has entered') ||
      lower.includes('got character') || lower.includes('entered the game')) return 'log-join';

  // Player leave
  if (lower.includes('left the game') || lower.includes('disconnected') ||
      lower.includes('lost connection') || lower.includes('timed out') ||
      lower.includes('left the server')) return 'log-leave';

  // Gamejanitor script messages
  if (/^\[[\w-]+\]/.test(msg)) return 'log-system';

  // Level-based coloring
  if (level === 'ERROR' || level === 'FATAL' || level === 'SEVERE') return 'log-msg-error';
  if (level === 'WARN') return 'log-msg-warn';
  if (level === 'DEBUG' || level === 'TRACE') return 'log-msg-debug';

  // Content-based fallback when no level was detected
  if (!level) {
    if (lower.includes('error') || lower.includes('exception') || lower.includes('failed')) return 'log-msg-error';
    if (lower.includes('warn') || lower.includes("can't keep up")) return 'log-msg-warn';
  }

  return 'log-msg';
}
