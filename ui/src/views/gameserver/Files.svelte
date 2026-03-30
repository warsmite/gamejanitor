<script lang="ts">
  import { api, type FileEntry } from '$lib/api';
  import { toast, confirm, prompt } from '$lib/stores';

  let { id }: { id: string } = $props();

  // Local state replaces URL search params for file navigation
  let currentPath = $state('/');
  let editing = $state(false);
  let editPath = $state('');
  let editContent = $state('');
  let editSaving = $state(false);

  let files = $state<FileEntry[]>([]);
  let loading = $state(true);

  // Rename state
  let renamingFile = $state('');
  let renameValue = $state('');

  // Upload refs
  let uploadInput: HTMLInputElement;
  let folderInput: HTMLInputElement;

  function buildSegments(path: string): { name: string; path: string }[] {
    const parts = path.split('/').filter(Boolean);
    const segments: { name: string; path: string }[] = [{ name: 'server', path: '/' }];
    let acc = '';
    for (const p of parts) {
      acc += '/' + p;
      segments.push({ name: p, path: acc });
    }
    return segments;
  }

  const pathSegments = $derived(buildSegments(currentPath));
  const editSegments = $derived(editing ? buildSegments(editPath) : []);

  // UI paths are display paths (/, /world). API expects /data, /data/world.
  function apiPath(displayPath: string): string {
    if (displayPath === '/') return '/data';
    return '/data' + displayPath;
  }

  // Sort: directories first, then alphabetical
  const sortedFiles = $derived(
    [...(files || [])].sort((a, b) => {
      if (a.is_dir !== b.is_dir) return a.is_dir ? -1 : 1;
      return a.name.localeCompare(b.name);
    })
  );

  // Load files on mount and when path changes
  let lastLoadedPath = '';
  $effect(() => {
    if (currentPath !== lastLoadedPath && !editing) {
      lastLoadedPath = currentPath;
      loadFiles();
    }
  });

  async function loadFiles() {
    loading = true;
    try {
      files = (await api.files.list(id, apiPath(currentPath))) || [];
    } catch (e: any) {
      toast(`Failed to load files: ${e.message}`, 'error');
      files = [];
    } finally {
      loading = false;
    }
  }

  function navigateTo(path: string) {
    renamingFile = '';
    editing = false;
    currentPath = path;
  }

  function openDir(name: string) {
    const newPath = currentPath === '/' ? `/${name}` : `${currentPath}/${name}`;
    navigateTo(newPath);
  }

  function filePath(name: string): string {
    return currentPath === '/' ? `/${name}` : `${currentPath}/${name}`;
  }

  async function openEditor(name: string) {
    const path = filePath(name);
    try {
      const result = await api.files.read(id, apiPath(path));
      editPath = path;
      editContent = result.content;
      editing = true;
    } catch (e: any) {
      toast(`Failed to read file: ${e.message}`, 'error');
    }
  }

  async function saveFile() {
    editSaving = true;
    try {
      await api.files.write(id, apiPath(editPath), editContent);
      toast('File saved', 'success');
    } catch (e: any) {
      toast(`Failed to save: ${e.message}`, 'error');
    } finally {
      editSaving = false;
    }
  }

  function closeEditor() {
    editing = false;
  }

  function handleEditorKeydown(e: KeyboardEvent) {
    if ((e.ctrlKey || e.metaKey) && e.key === 's') {
      e.preventDefault();
      saveFile();
    }
  }

  async function deleteFile(name: string, isDir: boolean) {
    const path = filePath(name);
    if (!await confirm({ title: `Delete ${isDir ? 'Directory' : 'File'}`, message: `Delete "${name}"? This cannot be undone.`, confirmLabel: 'Delete', danger: true })) return;
    try {
      await api.files.delete(id, apiPath(path));
      files = files.filter(f => f.name !== name);
    } catch (e: any) {
      toast(`Failed to delete: ${e.message}`, 'error');
    }
  }

  function startRename(name: string) {
    renamingFile = name;
    renameValue = name;
  }

  async function confirmRename(oldName: string) {
    if (!renamingFile || !renameValue || renameValue === oldName) {
      renamingFile = '';
      return;
    }
    renamingFile = '';
    const newName = renameValue;
    const from = filePath(oldName);
    const to = filePath(newName);
    try {
      await api.files.rename(id, apiPath(from), apiPath(to));
      files = files.map(f => f.name === oldName ? { ...f, name: newName } : f);
    } catch (e: any) {
      toast(`Failed to rename: ${e.message}`, 'error');
    }
  }

  async function createDirectory() {
    const name = await prompt({ title: 'New Folder', placeholder: 'Folder name', confirmLabel: 'Create' });
    if (!name) return;
    const path = filePath(name);
    try {
      await api.files.mkdir(id, apiPath(path));
      files = [...files, { name, is_dir: true, size: 0, mod_time: new Date().toISOString(), permissions: '' }];
    } catch (e: any) {
      toast(`Failed to create directory: ${e.message}`, 'error');
    }
  }

  async function createFile() {
    const name = await prompt({ title: 'New File', placeholder: 'File name', confirmLabel: 'Create' });
    if (!name) return;
    const path = filePath(name);
    try {
      await api.files.write(id, apiPath(path), '');
      files = [...files, { name, is_dir: false, size: 0, mod_time: new Date().toISOString(), permissions: '' }];
      if (isTextFile(name)) {
        openEditor(name);
      }
    } catch (e: any) {
      toast(`Failed to create file: ${e.message}`, 'error');
    }
  }

  async function uploadFiles(e: Event) {
    const input = e.target as HTMLInputElement;
    const fileList = input.files;
    if (!fileList || fileList.length === 0) return;

    let succeeded = 0;
    let failed = 0;

    for (const file of fileList) {
      // For folder uploads, webkitRelativePath has the relative path (e.g. "mods/mymod.jar").
      // The backend creates parent directories automatically.
      const relativePath = (file as any).webkitRelativePath || '';
      const filename = relativePath || file.name;

      try {
        await api.files.upload(id, apiPath(currentPath), file, filename);
        succeeded++;
      } catch {
        failed++;
      }
    }

    if (succeeded > 0) {
      toast(`Uploaded ${succeeded} file${succeeded > 1 ? 's' : ''}${failed > 0 ? ` (${failed} failed)` : ''}`, succeeded > 0 && failed === 0 ? 'success' : 'warning');
      await loadFiles();
    } else {
      toast('Upload failed', 'error');
    }

    input.value = '';
  }

  function downloadFile(name: string) {
    const path = filePath(name);
    const url = api.files.downloadUrl(id, apiPath(path));
    window.open(url, '_blank');
  }

  function isTextFile(name: string): boolean {
    const textExts = ['.txt', '.properties', '.yml', '.yaml', '.json', '.cfg', '.conf', '.ini', '.toml', '.log', '.sh', '.bat', '.cmd', '.xml', '.html', '.css', '.js', '.ts', '.md'];
    return textExts.some(ext => name.toLowerCase().endsWith(ext));
  }

  function fileIcon(name: string, isDir: boolean): string {
    if (isDir) return '📁';
    const lower = name.toLowerCase();
    const ext = lower.slice(lower.lastIndexOf('.'));
    const icons: Record<string, string> = {
      '.yml': '⚙', '.yaml': '⚙', '.properties': '⚙', '.cfg': '⚙',
      '.conf': '⚙', '.ini': '⚙', '.toml': '⚙', '.xml': '⚙',
      '.json': '{ }',
      '.log': '📋',
      '.jar': '☕', '.java': '☕',
      '.sh': '▸', '.bat': '▸', '.cmd': '▸',
      '.zip': '📦', '.tar': '📦', '.gz': '📦', '.7z': '📦', '.rar': '📦',
      '.png': '🖼', '.jpg': '🖼', '.jpeg': '🖼', '.gif': '🖼', '.ico': '🖼',
      '.txt': '📝', '.md': '📝',
      '.db': '🗃', '.sqlite': '🗃', '.dat': '🗃',
    };
    return icons[ext] || '📄';
  }

  function formatSize(bytes: number): string {
    if (bytes === 0) return '—';
    if (bytes < 1024) return `${bytes} B`;
    if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
    return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
  }

  function formatDate(iso: string): string {
    return new Date(iso).toLocaleDateString('en-US', { month: 'short', day: 'numeric', year: 'numeric' });
  }
</script>

{#if editing}
  <!-- File editor -->
  <div class="editor-toolbar">
    <div class="file-path">
      {#each editSegments as seg, i}
        {#if i > 0}
          <span class="file-path-sep">›</span>
        {/if}
        {#if i < editSegments.length - 1}
          <button class="file-path-seg" onclick={() => navigateTo(seg.path)}>{seg.name}</button>
        {:else}
          <span class="file-path-seg current">{seg.name}</span>
        {/if}
      {/each}
    </div>
    <div class="editor-actions">
      <button class="btn-solid" onclick={saveFile} disabled={editSaving} style="font-size:0.78rem; padding:6px 14px;">
        {editSaving ? 'Saving...' : 'Save'}
      </button>
      <button class="btn-accent" onclick={closeEditor} style="font-size:0.78rem; padding:6px 14px;">Close</button>
    </div>
  </div>
  <div class="editor-wrap">
    <textarea class="editor" bind:value={editContent} spellcheck="false" onkeydown={handleEditorKeydown}></textarea>
  </div>
{:else}
  <!-- File browser -->
  <div class="files-toolbar">
    <div class="file-path">
      {#each pathSegments as seg, i}
        {#if i > 0}
          <span class="file-path-sep">›</span>
        {/if}
        <button
          class="file-path-seg"
          class:current={i === pathSegments.length - 1}
          onclick={() => navigateTo(seg.path)}
        >{seg.name}</button>
      {/each}
    </div>
    <div class="files-actions">
      <button class="btn-accent" onclick={() => uploadInput.click()} style="font-size:0.78rem; padding:6px 12px;">
        <svg viewBox="0 0 16 16" fill="currentColor" width="12" height="12"><path d="M.5 9.9a.5.5 0 0 1 .5.5v2.5a1 1 0 0 0 1 1h12a1 1 0 0 0 1-1v-2.5a.5.5 0 0 1 1 0v2.5a2 2 0 0 1-2 2H2a2 2 0 0 1-2-2v-2.5a.5.5 0 0 1 .5-.5z"/><path d="M7.646 1.146a.5.5 0 0 1 .708 0l3 3a.5.5 0 0 1-.708.708L8.5 2.707V11.5a.5.5 0 0 1-1 0V2.707L5.354 4.854a.5.5 0 1 1-.708-.708l3-3z"/></svg>
        Upload Files
      </button>
      <button class="btn-accent" onclick={() => folderInput.click()} style="font-size:0.78rem; padding:6px 12px;">
        <svg viewBox="0 0 16 16" fill="currentColor" width="12" height="12"><path d="M.5 9.9a.5.5 0 0 1 .5.5v2.5a1 1 0 0 0 1 1h12a1 1 0 0 0 1-1v-2.5a.5.5 0 0 1 1 0v2.5a2 2 0 0 1-2 2H2a2 2 0 0 1-2-2v-2.5a.5.5 0 0 1 .5-.5z"/><path d="M7.646 1.146a.5.5 0 0 1 .708 0l3 3a.5.5 0 0 1-.708.708L8.5 2.707V11.5a.5.5 0 0 1-1 0V2.707L5.354 4.854a.5.5 0 1 1-.708-.708l3-3z"/></svg>
        Upload Folder
      </button>
      <button class="btn-accent" onclick={createFile} style="font-size:0.78rem; padding:6px 12px;">
        <svg viewBox="0 0 16 16" fill="currentColor" width="12" height="12"><path d="M8 2a.75.75 0 0 1 .75.75v4.5h4.5a.75.75 0 0 1 0 1.5h-4.5v4.5a.75.75 0 0 1-1.5 0v-4.5h-4.5a.75.75 0 0 1 0-1.5h4.5v-4.5A.75.75 0 0 1 8 2z"/></svg>
        New File
      </button>
      <button class="btn-accent" onclick={createDirectory} style="font-size:0.78rem; padding:6px 12px;">
        <svg viewBox="0 0 16 16" fill="currentColor" width="12" height="12"><path d="M8 2a.75.75 0 0 1 .75.75v4.5h4.5a.75.75 0 0 1 0 1.5h-4.5v4.5a.75.75 0 0 1-1.5 0v-4.5h-4.5a.75.75 0 0 1 0-1.5h4.5v-4.5A.75.75 0 0 1 8 2z"/></svg>
        New Folder
      </button>
    </div>
    <input type="file" multiple bind:this={uploadInput} onchange={uploadFiles} style="display:none;">
    <input type="file" bind:this={folderInput} onchange={uploadFiles} style="display:none;" webkitdirectory>
  </div>

  <div class="file-panel">
    {#if loading}
      <div class="file-row"><span class="file-name" style="color:var(--text-tertiary)">Loading...</span></div>
    {:else if sortedFiles.length === 0}
      <div class="file-row"><span class="file-name" style="color:var(--text-tertiary)">Empty directory</span></div>
    {:else}
      {#each sortedFiles as file (file.name)}
        <div class="file-row" class:dir={file.is_dir}>
          <span class="file-icon">{fileIcon(file.name, file.is_dir)}</span>
          {#if renamingFile === file.name}
            <input
              class="rename-input"
              type="text"
              bind:value={renameValue}
              onkeydown={(e) => { if (e.key === 'Enter') confirmRename(file.name); if (e.key === 'Escape') renamingFile = ''; }}
              onblur={() => confirmRename(file.name)}
            >
          {:else}
            <span
              class="file-name"
              class:clickable={file.is_dir || isTextFile(file.name)}
              onclick={() => file.is_dir ? openDir(file.name) : (isTextFile(file.name) ? openEditor(file.name) : null)}
              role={file.is_dir || isTextFile(file.name) ? 'button' : undefined}
              tabindex={file.is_dir || isTextFile(file.name) ? 0 : undefined}
              onkeydown={(e) => e.key === 'Enter' && (file.is_dir ? openDir(file.name) : openEditor(file.name))}
            >{file.name}</span>
          {/if}
          <span class="file-size">{file.is_dir ? '—' : formatSize(file.size)}</span>
          <span class="file-date">{formatDate(file.mod_time)}</span>
          <div class="file-actions">
            {#if !file.is_dir && isTextFile(file.name)}
              <button class="file-act" onclick={() => openEditor(file.name)}>Edit</button>
            {/if}
            {#if !file.is_dir}
              <button class="file-act" onclick={() => downloadFile(file.name)}>Download</button>
            {/if}
            <button class="file-act" onclick={() => startRename(file.name)}>Rename</button>
            <button class="file-act danger" onclick={() => deleteFile(file.name, file.is_dir)}>Delete</button>
          </div>
        </div>
      {/each}
    {/if}
  </div>
{/if}

<style>
  @keyframes fade-up {
    from { opacity: 0; transform: translateY(8px); }
    to { opacity: 1; transform: translateY(0); }
  }

  .files-toolbar {
    display: flex; align-items: center; justify-content: space-between;
    margin-bottom: 12px;
    animation: fade-up 0.4s cubic-bezier(0.16, 1, 0.3, 1);
  }

  .file-path {
    display: flex; align-items: center; gap: 2px;
    font-size: 0.82rem; font-family: var(--font-mono);
  }
  .file-path-seg {
    color: var(--text-tertiary); background: none; border: none;
    padding: 3px 6px; border-radius: 3px;
    font-family: var(--font-mono); font-size: 0.82rem;
    cursor: pointer;
    transition: color 0.15s, background 0.15s;
  }
  .file-path-seg:hover { color: var(--accent); background: var(--accent-subtle); }
  .file-path-seg.current { color: var(--text-primary); cursor: default; }
  .file-path-sep { color: var(--text-tertiary); opacity: 0.3; font-size: 0.75rem; }

  .files-actions { display: flex; gap: 6px; }

  .file-panel {
    background: var(--bg-surface);
    border: 1px solid var(--border-subtle);
    border-radius: var(--radius);
    overflow: hidden;
    animation: fade-up 0.4s cubic-bezier(0.16, 1, 0.3, 1) 0.05s both;
  }

  .file-row {
    display: flex; align-items: center;
    padding: 10px 18px;
    gap: 12px;
    transition: background 0.12s;
    border-left: 2px solid transparent;
  }
  .file-row:hover { background: var(--bg-elevated); border-left-color: var(--accent); }
  .file-row + .file-row { border-top: 1px solid var(--border-dim); }

  .file-icon { font-size: 1rem; width: 20px; text-align: center; flex-shrink: 0; }
  .file-name {
    font-size: 0.84rem; font-weight: 500; flex: 1; min-width: 0;
    cursor: default; white-space: nowrap; overflow: hidden; text-overflow: ellipsis;
  }
  .file-row.dir .file-name { color: var(--accent); cursor: pointer; }
  .file-name.clickable { cursor: pointer; }
  .file-name.clickable:hover { color: var(--accent); }

  .rename-input {
    flex: 1; min-width: 0;
    padding: 4px 8px; border-radius: 4px;
    background: var(--bg-inset); border: 1px solid var(--accent-border);
    color: var(--text-primary); font-family: var(--font-body); font-size: 0.84rem;
    outline: none;
  }

  .file-size {
    font-size: 0.74rem; font-family: var(--font-mono);
    color: var(--text-tertiary); min-width: 70px; text-align: right;
  }
  .file-date {
    font-size: 0.74rem; font-family: var(--font-mono);
    color: var(--text-tertiary); min-width: 100px; text-align: right;
  }

  .file-actions {
    display: flex; gap: 2px; justify-content: flex-end;
    min-width: 200px;
    opacity: 0; transition: opacity 0.15s;
  }
  .file-row:hover .file-actions { opacity: 1; }

  .file-act {
    padding: 4px 8px; border-radius: 3px;
    font-size: 0.68rem; font-family: var(--font-mono);
    color: var(--text-tertiary); background: none; border: none;
    cursor: pointer; transition: color 0.15s, background 0.15s;
  }
  .file-act:hover { color: var(--accent); background: var(--accent-subtle); }
  .file-act.danger:hover { color: var(--danger); background: rgba(239,68,68,0.06); }

  /* Editor */
  .editor-toolbar {
    display: flex; align-items: center; justify-content: space-between;
    margin-bottom: 12px;
    animation: fade-up 0.4s cubic-bezier(0.16, 1, 0.3, 1);
  }
  .editor-actions { display: flex; gap: 6px; }

  .editor-wrap {
    background: var(--bg-surface);
    border: 1px solid var(--border-subtle);
    border-radius: var(--radius);
    overflow: hidden;
    animation: fade-up 0.4s cubic-bezier(0.16, 1, 0.3, 1) 0.05s both;
  }
  .editor {
    width: 100%; height: 500px;
    padding: 16px 18px;
    background: var(--bg-inset); border: none;
    color: var(--text-primary);
    font-family: var(--font-mono); font-size: 0.82rem;
    line-height: 1.6;
    resize: vertical; outline: none;
    tab-size: 4;
  }

  @media (max-width: 700px) {
    .files-toolbar { flex-direction: column; align-items: flex-start; gap: 10px; }
    .file-date { display: none; }
    .file-actions { opacity: 1; }
    .editor { height: 350px; }
  }
</style>
