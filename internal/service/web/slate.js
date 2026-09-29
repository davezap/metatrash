(() => {
  'use strict';

  const APP_VERSION = '0.9.2';
  const DB_NAME = 'slate-markdown-editor-db';
  const DB_VERSION = 1;
  const NODE_STORE = 'documents';
  const ATTACHMENT_STORE = 'attachments';
  const DOCUMENT_ID = 'document';
  const EDITOR_TEMPLATE = `<div id="app" class="editor-app" data-editor-mode="edit" data-read-only="false" data-document-complete="false">
    <section id="noteWorkspace" class="note-workspace">
      <div id="formatToolbar" class="format-toolbar" aria-label="Markdown editor controls">
        <div id="formatControls" class="format-controls" aria-label="Markdown formatting">
          <button type="button" data-format="bold" title="Bold (Ctrl+B)"><strong>B</strong></button>
          <button type="button" data-format="italic" title="Italic (Ctrl+I)"><em>I</em></button>
          <button type="button" data-format="strike" title="Strikethrough"><s>S</s></button>
          <span class="toolbar-separator"></span>
          <button type="button" data-format="heading" title="Heading">H</button>
          <button type="button" data-format="quote" title="Blockquote">❯</button>
          <button type="button" data-format="code" title="Inline code">&lt;/&gt;</button>
          <button type="button" data-format="codeblock" title="Code block">▣</button>
          <span class="toolbar-separator"></span>
          <button type="button" data-format="link" title="Link (Ctrl+K)">↗</button>
          <button type="button" data-format="wikilink" title="Wiki link">[[ ]]</button>
          <button type="button" data-format="task" title="Task">☑</button>
          <button type="button" data-format="video" title="Insert video Markdown template">▶</button>
          <button type="button" data-format="video-task" title="Insert locked video-task Markdown template">▶✓</button>
          <button id="attachButton" type="button" title="Attach image, video, or file">📎</button>
          <input id="attachInput" type="file" multiple hidden>
        </div>

        <div class="view-switcher" role="group" aria-label="Editor mode">
          <button class="view-button active" data-workspace-mode="edit" type="button" title="Edit with Obsidian-style Live Preview">Edit</button>
          <button class="view-button" data-workspace-mode="preview" type="button" title="Preview the rendered document without editing">Preview</button>
          <button class="view-button" data-workspace-mode="source" type="button" title="Edit the complete raw Markdown source">Source</button>
        </div>
      </div>

      <div id="workspaceBody" class="workspace-body">
        <div class="workspace-gutter" aria-label="Document navigation controls">
          <button id="outlineToggle" class="outline-toggle outline-control" type="button"
            aria-controls="documentOutline" aria-expanded="true"
            aria-label="Toggle document outline" title="Show or hide document outline">
            <svg class="outline-control-icon outline-menu-icon" viewBox="0 0 24 24" aria-hidden="true" focusable="false">
              <path d="M4 6h16M4 12h16M4 18h16"></path>
            </svg>
            <svg class="outline-control-icon outline-complete-icon" viewBox="0 0 24 24" aria-hidden="true" focusable="false">
              <path d="M4.5 12.5 9.5 17.5 19.5 6.5"></path>
            </svg>
          </button>
        </div>

        <aside id="documentOutline" class="document-outline" aria-label="Document navigation">
          <div class="outline-header">
            <div>
              <strong>Document navigation</strong>
              <span id="outlineSummary">No headings</span>
            </div>
            <button id="outlineClose" class="outline-close outline-control" type="button"
              aria-controls="documentOutline" aria-expanded="true"
              aria-label="Close document navigation" title="Close document navigation">
              <svg class="outline-control-icon outline-menu-icon" viewBox="0 0 24 24" aria-hidden="true" focusable="false">
                <path d="M4 6h16M4 12h16M4 18h16"></path>
              </svg>
              <svg class="outline-control-icon outline-complete-icon" viewBox="0 0 24 24" aria-hidden="true" focusable="false">
                <path d="M4.5 12.5 9.5 17.5 19.5 6.5"></path>
              </svg>
            </button>
          </div>
          <nav id="outlineNavigation" class="outline-navigation" aria-label="Headings"></nav>
        </aside>

        <div id="editorGrid" class="editor-grid live-view">
          <article id="previewPane" class="preview-pane live-editor markdown-body" aria-label="Markdown editor" contenteditable="false" spellcheck="true" data-placeholder="Start writing Markdown…"></article>
          <div id="editorPane" class="editor-pane">
            <textarea id="markdownEditor" class="markdown-editor" aria-label="Markdown source editor" spellcheck="true" placeholder="Start writing Markdown…"></textarea>
          </div>
          <div id="dropOverlay" class="drop-overlay" hidden>
            <div>
              <span class="drop-icon">⇩</span>
              <strong>Drop files to insert</strong>
              <small>Images and videos appear inline; other files become links.</small>
            </div>
          </div>
        </div>
      </div>

      <footer class="statusbar">
        <span id="saveStatus">Saved locally</span>
        <span id="documentStats">0 words · 0 characters</span>
      </footer>
    </section>

    <div id="toastRegion" class="toast-region" aria-live="polite" aria-atomic="true"></div>
  </div>`;
  const memorySettings = new Map();
  const externalScriptPromises = new Map();

  let mountHost = null;
  let mountRoot = null;
  let ownerDocument = document;
  let mountedApi = null;
  let initialisePromise = null;
  let resolveReady;
  let rejectReady;
  const readyPromise = new Promise((resolve, reject) => { resolveReady = resolve; rejectReady = reject; });
  let pendingAttachmentHandler = null;
  let currentMountOptions = {};

  const state = {
    db: null,
    nodes: [],
    selectedId: DOCUMENT_ID,
    view: 'live',
    editorMode: 'edit',
    readOnly: false,
    lastWritableWorkspaceMode: null,
    saveTimers: new Map(),
    previewTimer: null,
    outlineTimer: null,
    outlineOpen: true,
    completionState: Object.freeze({ hasCheckboxes: false, total: 0, checked: 0, complete: false }),
    completionCallbacks: new Set(),
    dragDepth: 0,
    previewUrls: [],
    isSaving: false,
    liveRange: null,
    activeLiveBlock: null,
    liveRenderGeneration: 0,
    suppressLiveFocusOut: 0,
    mediaPlayers: [],
    memoryStores: new Map([[NODE_STORE, new Map()], [ATTACHMENT_STORE, new Map()]]),
    usingMemoryStorage: false,
    persistDocument: true,
    persistSettings: true,
    attachmentHandler: null,
    attachmentBusy: false,
    interactiveTasks: false,
  };

  const el = {};

  const facade = {
    mount,
    get ready() { return readyPromise; },
    setAttachmentHandler(handler) {
      validateAttachmentHandler(handler);
      pendingAttachmentHandler = handler;
      if (mountedApi) mountedApi.setAttachmentHandler(handler);
    },
  };
  Object.defineProperty(facade, 'documentComplete', {
    enumerable: true,
    get() { return mountedApi?.documentComplete || false; },
  });
  window.SlateMarkdownEditor = facade;

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', autoMount, { once: true });
  } else {
    queueMicrotask(autoMount);
  }

  function autoMount() {
    if (initialisePromise) return;
    const target = document.querySelector('[data-slate-editor-autostart]');
    if (!target) return;
    const markdownNode = target.querySelector('script[type="text/markdown"]');
    const options = {};
    if (markdownNode) options.markdown = markdownNode.textContent || '';
    mount(target, options).catch(error => console.error(error));
  }

  function resolveMountTarget(target) {
    const resolved = typeof target === 'string' ? document.querySelector(target) : target;
    if (!(resolved instanceof Element)) {
      throw new TypeError('SlateMarkdownEditor.mount(target) requires an Element or matching selector.');
    }
    return resolved;
  }

  function validateAttachmentHandler(handler) {
    if (handler !== null && typeof handler !== 'function') {
      throw new TypeError('setAttachmentHandler requires a function or null.');
    }
  }

  async function mount(target, options = {}) {
    const resolved = resolveMountTarget(target);
    if (initialisePromise) {
      if (resolved !== mountHost) throw new Error('Slate 0.9.2 currently supports one mounted editor per page.');
      return initialisePromise;
    }

    validateAttachmentHandler(options.attachmentHandler ?? pendingAttachmentHandler);
    mountHost = resolved;
    mountRoot = resolved;
    ownerDocument = resolved.ownerDocument || document;
    currentMountOptions = { ...options };

    state.persistDocument = options.persist !== false;
    state.persistSettings = options.persistSettings ?? state.persistDocument;
    state.attachmentHandler = options.attachmentHandler ?? pendingAttachmentHandler;
    state.interactiveTasks = Boolean(options.interactiveTasks);

    const requestedMode = normaliseWorkspaceMode(
      options.workspaceMode ?? readSetting('slate.workspaceMode') ?? 'edit'
    );
    state.view = requestedMode === 'source'
      ? 'source'
      : normaliseStoredView(readSetting('slate.editorView'));
    state.editorMode = requestedMode === 'preview'
      ? 'view'
      : normaliseStoredEditorMode(readSetting('slate.editorMode'));
    if (requestedMode === 'edit') { state.view = 'live'; state.editorMode = 'edit'; }
    if (requestedMode === 'source') { state.view = 'source'; state.editorMode = 'edit'; }
    state.outlineOpen = options.outlineOpen === undefined
      ? normaliseStoredOutlineOpen(readSetting('slate.outlineOpen'))
      : Boolean(options.outlineOpen);

    mountHost.classList.add('slate-editor-host');
    mountHost.dataset.slateVersion = APP_VERSION;
    mountHost.innerHTML = EDITOR_TEMPLATE;

    initialisePromise = initialise(options).then(() => mountedApi);
    return initialisePromise;
  }

  async function initialise(options = {}) {
    cacheElements();
    bindEvents();
    applyStoredTheme(options.theme);

    try {
      state.db = await openDatabase();
      let documentRecord = null;

      if (state.persistDocument) {
        documentRecord = await getRecord(NODE_STORE, DOCUMENT_ID);
      }
      if (!documentRecord) documentRecord = createDefaultDocument();
      if (Object.prototype.hasOwnProperty.call(options, 'markdown')) {
        documentRecord.content = String(options.markdown ?? '');
      }
      documentRecord.content = ensureTrailingBlankLine(documentRecord.content || '');
      documentRecord.updatedAt = Date.now();
      if (state.persistDocument) await putRecord(NODE_STORE, documentRecord);

      state.nodes = [documentRecord];
      state.selectedId = DOCUMENT_ID;
      el.markdownEditor.value = documentRecord.content;
      updateDocumentStats();
      applyOutlineVisibility(state.outlineOpen, { persist: false });
      applyView(state.view, { render: false });
      await renderPreview();
      applyEditorMode(state.editorMode, { render: false });
      state.lastWritableWorkspaceMode = getWorkspaceMode() === 'source' ? 'source' : 'edit';
      exposePublicApi();
      if (options.readOnly) await applyReadOnly(true);
      if (!state.persistDocument) el.saveStatus.textContent = 'Host controlled';
      if (options.showReadyToast !== false) {
        const storageNote = state.persistDocument
          ? (state.usingMemoryStorage ? ' · session storage' : '')
          : ' · host controlled';
        showToast(`Slate Markdown Editor ${APP_VERSION} ready${storageNote}`);
      }
      dispatchEditorEvent('slate:ready', { api: mountedApi, version: APP_VERSION });
      resolveReady(mountedApi);
    } catch (error) {
      console.error(error);
      showFatalError(error);
      rejectReady(error);
      throw error;
    }
  }

  function cacheElements() {
    const ids = [
      'app', 'noteWorkspace', 'formatToolbar', 'formatControls', 'workspaceBody', 'documentOutline', 'outlineToggle',
      'outlineClose', 'outlineNavigation', 'outlineSummary', 'editorGrid', 'editorPane', 'markdownEditor',
      'previewPane', 'dropOverlay', 'saveStatus', 'documentStats', 'attachButton',
      'attachInput', 'toastRegion'
    ];
    ids.forEach(id => { el[id] = mountRoot.querySelector(`#${id}`); });
    el.viewButtons = [...mountRoot.querySelectorAll('.view-button')];
  }

  function bindEvents() {
    el.markdownEditor.addEventListener('input', () => {
      if (state.editorMode !== 'edit') return;
      const note = selectedNote();
      if (!note) return;

      const selectionStart = el.markdownEditor.selectionStart;
      const selectionEnd = el.markdownEditor.selectionEnd;
      const content = ensureTrailingBlankLine(el.markdownEditor.value);
      if (content !== el.markdownEditor.value) {
        el.markdownEditor.value = content;
        el.markdownEditor.setSelectionRange(selectionStart, selectionEnd);
      }

      note.content = content;
      note.updatedAt = Date.now();
      scheduleSave(note);
      schedulePreview();
      updateDocumentStats();
      dispatchDocumentChange();
    });

    el.markdownEditor.addEventListener('keydown', handleEditorKeydown);
    el.markdownEditor.addEventListener('paste', handlePaste);

    el.previewPane.addEventListener('pointerdown', handleLivePointerDown);
    el.previewPane.addEventListener('input', handleLiveInput);
    el.previewPane.addEventListener('keydown', handleLiveKeydown);
    el.previewPane.addEventListener('paste', handlePaste);
    el.previewPane.addEventListener('focusout', handleLiveFocusOut);
    el.previewPane.addEventListener('click', handlePreviewClick);
    el.previewPane.addEventListener('change', handlePreviewChange);

    el.formatToolbar.addEventListener('mousedown', event => {
      if (event.target.closest('button')) event.preventDefault();
    });
    el.formatToolbar.addEventListener('click', event => {
      if (state.editorMode !== 'edit') return;
      const button = event.target.closest('button[data-format]');
      if (button) applyFormatting(button.dataset.format);
    });

    el.attachButton.addEventListener('click', () => {
      if (state.editorMode === 'edit' && !state.attachmentBusy) el.attachInput.click();
    });
    el.attachInput.addEventListener('change', async event => {
      if (state.editorMode === 'edit') await insertFiles([...event.target.files], 'picker');
      event.target.value = '';
    });

    el.viewButtons.forEach(button => {
      button.addEventListener('click', () => applyWorkspaceMode(button.dataset.workspaceMode));
    });

    el.outlineToggle.addEventListener('click', () => applyOutlineVisibility(!state.outlineOpen));
    el.outlineClose.addEventListener('click', () => applyOutlineVisibility(false));
    el.outlineNavigation.addEventListener('click', handleOutlineNavigationClick);

    mountRoot.addEventListener('keydown', handleGlobalKeydown);
    ownerDocument.addEventListener('pointerdown', handleDocumentPointerDown, true);
    ownerDocument.addEventListener('selectionchange', rememberLiveSelection);
    window.addEventListener('beforeunload', flushPendingSave);
    mountRoot.addEventListener('dragenter', handleDragEnter);
    mountRoot.addEventListener('dragover', handleDragOver);
    mountRoot.addEventListener('dragleave', handleDragLeave);
    mountRoot.addEventListener('drop', handleDrop);
  }

  function openDatabase() {
    return new Promise((resolve, reject) => {
      let request;
      try {
        request = indexedDB.open(DB_NAME, DB_VERSION);
      } catch (error) {
        state.usingMemoryStorage = true;
        resolve(null);
        return;
      }

      request.onupgradeneeded = () => {
        const db = request.result;
        if (!db.objectStoreNames.contains(NODE_STORE)) {
          db.createObjectStore(NODE_STORE, { keyPath: 'id' });
        }
        if (!db.objectStoreNames.contains(ATTACHMENT_STORE)) {
          db.createObjectStore(ATTACHMENT_STORE, { keyPath: 'id' });
        }
      };
      request.onsuccess = () => resolve(request.result);
      request.onerror = () => {
        console.warn('Persistent browser storage is unavailable; using memory storage for this session.', request.error);
        state.usingMemoryStorage = true;
        resolve(null);
      };
      request.onblocked = () => reject(new Error('The editor database is open in another tab and cannot be upgraded.'));
    });
  }


  function transaction(storeName, mode = 'readonly') {
    if (!state.db) throw new Error('Persistent storage is unavailable.');
    return state.db.transaction(storeName, mode).objectStore(storeName);
  }


  function getRecord(storeName, id) {
    if (state.usingMemoryStorage || !state.db) {
      return Promise.resolve(state.memoryStores.get(storeName)?.get(id) || null);
    }
    return new Promise((resolve, reject) => {
      const request = transaction(storeName).get(id);
      request.onsuccess = () => resolve(request.result || null);
      request.onerror = () => reject(request.error);
    });
  }

  function putRecord(storeName, record) {
    if (state.usingMemoryStorage || !state.db) {
      if (!state.memoryStores.has(storeName)) state.memoryStores.set(storeName, new Map());
      state.memoryStores.get(storeName).set(record.id, record);
      return Promise.resolve(record);
    }
    return new Promise((resolve, reject) => {
      const request = transaction(storeName, 'readwrite').put(record);
      request.onsuccess = () => resolve(record);
      request.onerror = () => reject(request.error);
    });
  }




  function uuid() {
    if (crypto && typeof crypto.randomUUID === 'function') return crypto.randomUUID();
    return `id-${Date.now()}-${Math.random().toString(16).slice(2)}`;
  }

  function selectedNode() {
    return state.nodes[0] || null;
  }


  function selectedNote() {
    return selectedNode();
  }

  function createDefaultDocument() {
    const now = Date.now();
    return {
      id: DOCUMENT_ID,
      type: 'document',
      content: ensureTrailingBlankLine(`# Slate Markdown Editor

This is a **reusable Markdown editor** with Obsidian-style Live Preview.

## Editing

- Use **Edit** for Obsidian-style Live Preview.
- Use **Preview** to view the rendered document without editing.
- Use **Source** to edit the complete raw Markdown.
- Drag or paste images, videos, and files into the editor.

## Video task

Use \`- [>] !video[Required training](url)\` for a locked watch-completion task.
`),
      createdAt: now,
      updatedAt: now,
    };
  }


  function ensureTrailingBlankLine(markdown) {
    const text = String(markdown ?? '').replace(/\r\n?/g, '\n');
    if (!text || text.endsWith('\n')) return text;
    return `${text}\n`;
  }

  function ensureTrailingLiveBlankBlock() {
    if (!el.previewPane) return null;
    const blocks = [...el.previewPane.querySelectorAll(':scope > .live-block')];
    const last = blocks.at(-1);
    if (last && (last.dataset.source || '') === '') return last;
    const trailing = createLiveBlock('');
    el.previewPane.appendChild(trailing);
    return trailing;
  }

















  function scheduleSave(note) {
    if (!state.persistDocument) {
      state.isSaving = false;
      el.saveStatus.textContent = 'Host controlled';
      return;
    }

    state.isSaving = true;
    el.saveStatus.textContent = 'Saving…';

    const existing = state.saveTimers.get(note.id);
    if (existing) clearTimeout(existing.timer);

    const snapshot = { ...note };
    const timer = setTimeout(async () => {
      state.saveTimers.delete(note.id);
      try {
        await putRecord(NODE_STORE, snapshot);
        state.isSaving = state.saveTimers.size > 0;
        el.saveStatus.textContent = state.isSaving ? 'Saving…' : 'Saved locally';
      } catch (error) {
        state.isSaving = state.saveTimers.size > 0;
        el.saveStatus.textContent = 'Save failed';
        showToast(`Could not save: ${error.message}`, true);
      }
    }, 350);

    state.saveTimers.set(note.id, { timer, snapshot });
  }

  async function flushPendingSave() {
    if (!state.persistDocument || !state.db || state.saveTimers.size === 0) return;
    const pending = [...state.saveTimers.values()];
    state.saveTimers.clear();
    pending.forEach(item => clearTimeout(item.timer));

    try {
      await Promise.all(pending.map(item => putRecord(NODE_STORE, item.snapshot)));
      state.isSaving = false;
      el.saveStatus.textContent = 'Saved locally';
    } catch (error) {
      state.isSaving = false;
      el.saveStatus.textContent = 'Save failed';
      console.error(error);
    }
  }

  function schedulePreview() {
    clearTimeout(state.previewTimer);
    state.previewTimer = setTimeout(renderPreview, 120);
  }

  async function renderPreview() {
    const note = selectedNote();
    if (!note) return;

    state.activeLiveBlock = null;
    state.liveRange = null;
    destroyMediaPlayers();
    revokePreviewUrls();
    const generation = ++state.liveRenderGeneration;
    const fragment = document.createDocumentFragment();

    const content = ensureTrailingBlankLine(note.content || '');
    splitMarkdownIntoLiveBlocks(content).forEach(source => {
      fragment.appendChild(createLiveBlock(source));
    });

    el.previewPane.replaceChildren(fragment);
    ensureTrailingLiveBlankBlock();
    await hydrateAttachments();
    if (generation !== state.liveRenderGeneration) return;
    hydrateWikiLinks();
    await hydrateVideoPlayers();
    refreshLiveBlockLineIndexes();
    applyPreviewInteractivity();
    updateDocumentOutline();
  }


  function splitMarkdownIntoLiveBlocks(markdown) {
    const lines = String(markdown).replace(/\r\n?/g, '\n').split('\n');
    const blocks = [];
    let index = 0;

    while (index < lines.length) {
      const line = lines[index];

      if (/^\s*```/.test(line)) {
        const code = [line];
        index += 1;
        while (index < lines.length) {
          code.push(lines[index]);
          const isClosingFence = /^\s*```/.test(lines[index]);
          index += 1;
          if (isClosingFence) break;
        }
        blocks.push(code.join('\n'));
        continue;
      }

      if (isTableStart(lines, index)) {
        const table = [lines[index], lines[index + 1]];
        index += 2;
        while (index < lines.length && lines[index].trim() && lines[index].includes('|')) {
          table.push(lines[index]);
          index += 1;
        }
        blocks.push(table.join('\n'));
        continue;
      }

      blocks.push(line);
      index += 1;
    }

    return blocks.length ? blocks : [''];
  }

  function createLiveBlock(source = '') {
    const block = document.createElement('div');
    block.className = 'live-block';
    block.dataset.source = String(source);
    block.dataset.placeholder = 'Start writing Markdown…';
    block.contentEditable = 'false';
    block.spellcheck = true;
    renderLiveBlock(block);
    return block;
  }

  function renderLiveBlock(block) {
    if (!block) return;
    const source = block.dataset.source ?? '';
    block.className = 'live-block is-rendered';
    block.contentEditable = 'false';
    block.spellcheck = true;
    block.dataset.liveMode = 'source-map';
    block.replaceChildren();

    if (source === '') {
      block.classList.add('is-empty');
      block.innerHTML = '<br>';
      return;
    }

    if (renderSourcePreservingBlock(block, source)) return;

    block.dataset.liveMode = 'raw';
    block.innerHTML = renderMarkdown(source);
  }

  function renderSourcePreservingBlock(block, source) {
    if (source.includes('\n')) return false;

    const video = parseStandaloneVideo(source);
    if (video) {
      renderLiveVideoBlock(block, source, video);
      return true;
    }

    const image = parseStandaloneImage(source);
    if (image) {
      renderLiveImageBlock(block, source, image);
      return true;
    }

    const heading = source.match(/^(#{1,6})(\s+)(.*)$/);
    if (heading) {
      const level = heading[1].length;
      const headingElement = document.createElement(`h${level}`);
      headingElement.className = 'live-heading md-element md-block-element';
      setMarkdownRange(headingElement, 'heading', 0, source.length, heading[1].length + heading[2].length, source.length);
      headingElement.appendChild(createMarkdownMarker(heading[1] + heading[2]));
      appendInlineSource(headingElement, heading[3], heading[1].length + heading[2].length);
      block.appendChild(headingElement);
      return true;
    }

    const task = source.match(/^(\s*[-+*]\s+)\[([ xX])\](\s+)(.*)$/);
    if (task) {
      const prefix = `${task[1]}[${task[2]}]${task[3]}`;
      const row = document.createElement('div');
      row.className = 'live-task-line md-element md-block-element';
      setMarkdownRange(row, 'task', 0, source.length, prefix.length, source.length);

      const checkbox = document.createElement('input');
      checkbox.type = 'checkbox';
      checkbox.className = 'task-checkbox';
      checkbox.checked = task[2].toLowerCase() === 'x';
      checkbox.contentEditable = 'false';
      checkbox.dataset.sourceIgnore = 'true';
      row.appendChild(checkbox);
      row.appendChild(createMarkdownMarker(prefix));

      const content = document.createElement('span');
      content.className = 'live-line-content';
      appendInlineSource(content, task[4], prefix.length);
      row.appendChild(content);
      block.appendChild(row);
      return true;
    }

    const list = source.match(/^(\s*)((?:[-+*])|(?:\d+\.))(\s+)(.*)$/);
    if (list) {
      const prefix = `${list[1]}${list[2]}${list[3]}`;
      const row = document.createElement('div');
      row.className = `live-list-line ${/\d+\./.test(list[2]) ? 'ordered' : 'unordered'} md-element md-block-element`;
      row.dataset.renderedMarker = /\d+\./.test(list[2]) ? list[2] : '•';
      setMarkdownRange(row, 'list', 0, source.length, prefix.length, source.length);
      row.appendChild(createMarkdownMarker(prefix));

      const content = document.createElement('span');
      content.className = 'live-line-content';
      appendInlineSource(content, list[4], prefix.length);
      row.appendChild(content);
      block.appendChild(row);
      return true;
    }

    const quote = source.match(/^(\s*>\s?)(.*)$/);
    if (quote) {
      const row = document.createElement('blockquote');
      row.className = 'live-quote-line md-element md-block-element';
      setMarkdownRange(row, 'quote', 0, source.length, quote[1].length, source.length);
      row.appendChild(createMarkdownMarker(quote[1]));
      const content = document.createElement('span');
      content.className = 'live-line-content';
      appendInlineSource(content, quote[2], quote[1].length);
      row.appendChild(content);
      block.appendChild(row);
      return true;
    }

    if (/^\s*((\*\s*){3,}|(-\s*){3,}|(_\s*){3,})\s*$/.test(source)) {
      block.dataset.liveMode = 'raw';
      block.innerHTML = '<hr>';
      return true;
    }

    const paragraph = document.createElement('p');
    paragraph.className = 'live-paragraph';
    appendInlineSource(paragraph, source, 0);
    block.appendChild(paragraph);
    return true;
  }

  function setMarkdownRange(element, kind, start, end, contentStart, contentEnd) {
    element.classList.add('md-element');
    element.dataset.mdElement = kind;
    element.dataset.mdStart = String(start);
    element.dataset.mdEnd = String(end);
    element.dataset.mdContentStart = String(contentStart);
    element.dataset.mdContentEnd = String(contentEnd);
  }

  function createMarkdownMarker(text) {
    const marker = document.createElement('span');
    marker.className = 'md-marker';
    marker.textContent = text;
    marker.setAttribute('aria-hidden', 'true');
    return marker;
  }

  function createEditableTextNode(text) {
    // Browsers may discard a regular trailing space in contenteditable nodes.
    // NBSP keeps the caret position stable; liveBlockText normalises it back to a space.
    return document.createTextNode(String(text).replace(/ +$/g, spaces => '\u00a0'.repeat(spaces.length)));
  }

  function appendInlineSource(parent, source, baseOffset = 0) {
    let cursor = 0;
    while (cursor < source.length) {
      const token = readInlineToken(source, cursor, baseOffset);
      if (token) {
        parent.appendChild(token.node);
        cursor = token.end;
        continue;
      }

      let next = cursor + 1;
      while (next < source.length && !isInlineTokenStart(source, next)) next += 1;
      parent.appendChild(createEditableTextNode(source.slice(cursor, next)));
      cursor = next;
    }

    if (parent.lastElementChild?.classList.contains('md-inline-element') && parent.lastChild === parent.lastElementChild) {
      parent.appendChild(document.createTextNode('\u200B'));
    }
  }

  function isInlineTokenStart(source, index) {
    const char = source[index];
    return char === '`' || char === '*' || char === '_' || char === '~' || char === '[' || char === '!';
  }

  function readInlineToken(source, index, baseOffset) {
    const rest = source.slice(index);

    const image = rest.match(/^!\[([^\]]*)\]\(([^)\s]+)(?:\s+"([^"]*)")?\)/);
    if (image) {
      const full = image[0];
      const wrapper = document.createElement('span');
      wrapper.className = 'md-inline-element live-inline-image';
      const contentStart = baseOffset + index + 2;
      const contentEnd = contentStart + image[1].length;
      setMarkdownRange(wrapper, 'image', baseOffset + index, baseOffset + index + full.length, contentStart, contentEnd);
      wrapper.appendChild(createMarkdownMarker(full));
      const imageElement = createRenderedImage(image[1], image[2], image[3] || '');
      imageElement.contentEditable = 'false';
      imageElement.dataset.sourceIgnore = 'true';
      wrapper.appendChild(imageElement);
      return { node: wrapper, end: index + full.length };
    }

    if (rest.startsWith('[[')) {
      const close = source.indexOf(']]', index + 2);
      if (close !== -1) {
        const inner = source.slice(index + 2, close);
        const pipe = inner.indexOf('|');
        const rawTarget = pipe === -1 ? inner : inner.slice(0, pipe);
        const noteName = rawTarget.trim();
        const display = pipe === -1 ? inner : inner.slice(pipe + 1);
        const wrapper = document.createElement('a');
        wrapper.href = '#';
        wrapper.className = 'wiki-link md-inline-element';
        wrapper.dataset.noteName = noteName;
        setMarkdownRange(wrapper, 'wikilink', baseOffset + index, baseOffset + close + 2, baseOffset + index + 2, baseOffset + close);
        wrapper.appendChild(createMarkdownMarker(pipe === -1 ? '[[' : `[[${inner.slice(0, pipe + 1)}`));
        wrapper.appendChild(createEditableTextNode(display));
        wrapper.appendChild(createMarkdownMarker(']]'));
        return { node: wrapper, end: close + 2 };
      }
    }

    if (rest.startsWith('[')) {
      const match = rest.match(/^\[([^\]]+)\]\(([^)\s]+)(?:\s+"([^"]*)")?\)/);
      if (match) {
        const full = match[0];
        const wrapper = document.createElement('a');
        const url = match[2];
        wrapper.className = 'md-inline-element';
        if (isAttachmentUrl(url)) {
          wrapper.classList.add('attachment-link');
          wrapper.dataset.attachmentId = attachmentIdFromUrl(url);
          wrapper.href = '#';
        } else {
          wrapper.href = safeUrl(url, false) || '#';
          wrapper.target = '_blank';
          wrapper.rel = 'noopener noreferrer';
        }
        if (match[3]) wrapper.title = match[3];
        setMarkdownRange(wrapper, 'link', baseOffset + index, baseOffset + index + full.length, baseOffset + index + 1, baseOffset + index + 1 + match[1].length);
        wrapper.appendChild(createMarkdownMarker('['));
        appendInlineSource(wrapper, match[1], baseOffset + index + 1);
        wrapper.appendChild(createMarkdownMarker(full.slice(match[1].length + 1)));
        return { node: wrapper, end: index + full.length };
      }
    }

    if (rest.startsWith('`')) {
      const close = source.indexOf('`', index + 1);
      if (close !== -1) {
        const wrapper = document.createElement('code');
        wrapper.className = 'md-inline-element';
        setMarkdownRange(wrapper, 'code', baseOffset + index, baseOffset + close + 1, baseOffset + index + 1, baseOffset + close);
        wrapper.appendChild(createMarkdownMarker('`'));
        wrapper.appendChild(createEditableTextNode(source.slice(index + 1, close)));
        wrapper.appendChild(createMarkdownMarker('`'));
        return { node: wrapper, end: close + 1 };
      }
    }

    for (const [delimiter, tagName, kind] of [['**', 'strong', 'bold'], ['__', 'strong', 'bold'], ['~~', 'del', 'strike']]) {
      if (!rest.startsWith(delimiter)) continue;
      const close = source.indexOf(delimiter, index + delimiter.length);
      if (close === -1) continue;
      const wrapper = document.createElement(tagName);
      wrapper.className = 'md-inline-element';
      setMarkdownRange(wrapper, kind, baseOffset + index, baseOffset + close + delimiter.length, baseOffset + index + delimiter.length, baseOffset + close);
      wrapper.appendChild(createMarkdownMarker(delimiter));
      appendInlineSource(wrapper, source.slice(index + delimiter.length, close), baseOffset + index + delimiter.length);
      wrapper.appendChild(createMarkdownMarker(delimiter));
      return { node: wrapper, end: close + delimiter.length };
    }

    for (const [delimiter, tagName, kind] of [['*', 'em', 'italic'], ['_', 'em', 'italic']]) {
      if (!rest.startsWith(delimiter) || rest.startsWith(delimiter + delimiter)) continue;
      const close = source.indexOf(delimiter, index + 1);
      if (close === -1) continue;
      const wrapper = document.createElement(tagName);
      wrapper.className = 'md-inline-element';
      setMarkdownRange(wrapper, kind, baseOffset + index, baseOffset + close + 1, baseOffset + index + 1, baseOffset + close);
      wrapper.appendChild(createMarkdownMarker(delimiter));
      appendInlineSource(wrapper, source.slice(index + 1, close), baseOffset + index + 1);
      wrapper.appendChild(createMarkdownMarker(delimiter));
      return { node: wrapper, end: close + 1 };
    }

    return null;
  }

  function parseStandaloneVideo(source) {
    const taskMatch = String(source).match(/^(\s*[-+*]\s+)\[([>xX])\](\s+)(!video\[[^\]]*\]\([^\n]+\))\s*$/);
    const videoSource = taskMatch ? taskMatch[4] : String(source).trim();
    const match = videoSource.match(/^!video\[([^\]]*)\]\(([^)\s]+)(?:\s+"([^"]*)")?\)$/i);
    if (!match) return null;

    return {
      label: match[1] || 'Video',
      target: match[2],
      title: match[3] || '',
      task: taskMatch ? {
        prefix: taskMatch[1],
        marker: taskMatch[2].toLowerCase(),
        spacing: taskMatch[3],
      } : null,
    };
  }

  function isMediaBlock(block) {
    return Boolean(block?.classList?.contains('is-image-block') || block?.classList?.contains('is-video-block'));
  }

  function videoTargetInfo(target) {
    const raw = String(target || '').trim();
    if (isAttachmentUrl(raw)) {
      return { provider: 'html5', attachmentId: attachmentIdFromUrl(raw), src: '' };
    }

    const remote = safeRemoteUrl(raw);
    if (!remote) return { provider: 'invalid', src: '' };

    const youtubeId = youtubeVideoId(remote);
    if (youtubeId) {
      const parameters = new URLSearchParams({ enablejsapi: '1', playsinline: '1', rel: '0' });
      if (/^https?:$/i.test(location.protocol)) parameters.set('origin', location.origin);
      return {
        provider: 'youtube',
        id: youtubeId,
        src: `https://www.youtube.com/embed/${encodeURIComponent(youtubeId)}?${parameters}`,
      };
    }

    const vimeoId = vimeoVideoId(remote);
    if (vimeoId) {
      return {
        provider: 'vimeo',
        id: vimeoId,
        src: `https://player.vimeo.com/video/${encodeURIComponent(vimeoId)}?dnt=1`,
      };
    }

    const loomId = loomVideoId(remote);
    if (loomId) {
      return {
        provider: 'generic',
        name: 'Loom',
        src: `https://www.loom.com/embed/${encodeURIComponent(loomId)}`,
      };
    }

    if (/\.(mp4|m4v|webm|ogv|ogg|mov)(?:$|[?#])/i.test(remote)) {
      return { provider: 'html5', src: remote };
    }

    return { provider: 'generic', name: 'embedded provider', src: remote };
  }

  function safeRemoteUrl(url) {
    try {
      const parsed = new URL(String(url).trim(), location.href);
      return /^(https?):$/i.test(parsed.protocol) ? parsed.href : '';
    } catch {
      return '';
    }
  }

  function youtubeVideoId(url) {
    try {
      const parsed = new URL(url);
      const host = parsed.hostname.toLowerCase().replace(/^www\./, '');
      if (host === 'youtu.be') return sanitiseVideoId(parsed.pathname.split('/').filter(Boolean)[0]);
      if (!['youtube.com', 'm.youtube.com', 'music.youtube.com', 'youtube-nocookie.com'].includes(host)) return '';
      if (parsed.pathname === '/watch') return sanitiseVideoId(parsed.searchParams.get('v'));
      const segments = parsed.pathname.split('/').filter(Boolean);
      if (['embed', 'shorts', 'live'].includes(segments[0])) return sanitiseVideoId(segments[1]);
    } catch {
      return '';
    }
    return '';
  }

  function vimeoVideoId(url) {
    try {
      const parsed = new URL(url);
      const host = parsed.hostname.toLowerCase().replace(/^www\./, '');
      if (!['vimeo.com', 'player.vimeo.com'].includes(host)) return '';
      const segments = parsed.pathname.split('/').filter(Boolean);
      const candidate = host === 'player.vimeo.com' && segments[0] === 'video' ? segments[1] : [...segments].reverse().find(value => /^\d+$/.test(value));
      return /^\d+$/.test(candidate || '') ? candidate : '';
    } catch {
      return '';
    }
  }

  function loomVideoId(url) {
    try {
      const parsed = new URL(url);
      const host = parsed.hostname.toLowerCase().replace(/^www\./, '');
      if (host !== 'loom.com') return '';
      const segments = parsed.pathname.split('/').filter(Boolean);
      if (!['share', 'embed'].includes(segments[0])) return '';
      return sanitiseVideoId(segments[1]);
    } catch {
      return '';
    }
  }

  function sanitiseVideoId(value) {
    const candidate = String(value || '').trim();
    return /^[A-Za-z0-9_-]{5,64}$/.test(candidate) ? candidate : '';
  }

  function renderLiveVideoBlock(block, source, videoData) {
    block.classList.add('is-video-block');
    block.contentEditable = 'false';

    const figure = document.createElement('figure');
    figure.className = 'live-media-figure live-video-figure';

    const sourceRow = document.createElement('div');
    sourceRow.className = 'media-source-row video-source-row';
    const sourceEditor = document.createElement('span');
    sourceEditor.className = 'media-source-editor video-source-editor';
    sourceEditor.textContent = source;
    sourceEditor.contentEditable = 'false';
    sourceEditor.spellcheck = false;
    sourceRow.appendChild(sourceEditor);

    const toggle = document.createElement('button');
    toggle.type = 'button';
    toggle.className = 'media-source-toggle video-source-toggle';
    toggle.title = 'Edit video Markdown';
    toggle.setAttribute('aria-label', 'Edit video Markdown');
    toggle.dataset.sourceIgnore = 'true';

    const frame = document.createElement('div');
    frame.className = 'live-media-frame live-video-frame';
    frame.dataset.sourceIgnore = 'true';

    const info = videoTargetInfo(videoData.target);
    frame.dataset.videoProvider = info.provider;
    frame.dataset.videoTarget = videoData.target;

    if (videoData.task) {
      const status = document.createElement('div');
      status.className = 'video-completion-status';
      const checkbox = document.createElement('input');
      checkbox.type = 'checkbox';
      checkbox.className = 'video-completion-checkbox';
      checkbox.checked = videoData.task.marker === 'x';
      checkbox.disabled = true;
      checkbox.tabIndex = -1;
      checkbox.setAttribute('aria-label', checkbox.checked ? 'Video completed' : 'Completes automatically when video ends');
      const label = document.createElement('span');
      label.className = 'video-completion-label';
      label.textContent = videoData.label || 'Video';
      const note = document.createElement('span');
      note.className = 'video-completion-note';
      note.textContent = checkbox.checked ? 'Completed' : 'Locked · completes at the end';
      status.append(checkbox, label, note);
      figure.appendChild(status);
      block.classList.toggle('is-video-complete', checkbox.checked);
      block.dataset.videoTask = 'true';
    }

    if (info.provider === 'html5') {
      const video = document.createElement('video');
      video.className = 'embedded-video html5-video';
      video.controls = true;
      video.preload = 'metadata';
      video.playsInline = true;
      video.setAttribute('aria-label', videoData.label || 'Embedded video');
      if (info.attachmentId) video.dataset.attachmentId = info.attachmentId;
      else video.src = info.src;
      video.dataset.videoProvider = 'html5';
      frame.appendChild(video);
    } else if (info.provider === 'youtube') {
      const host = document.createElement('div');
      host.className = 'embedded-video video-iframe youtube-player-host';
      host.dataset.videoProvider = 'youtube';
      host.dataset.videoId = info.id;
      host.dataset.videoSrc = info.src;
      host.dataset.videoTitle = videoData.title || videoData.label || 'Embedded video';
      host.textContent = 'Loading YouTube player…';
      frame.appendChild(host);
    } else if (['vimeo', 'generic'].includes(info.provider)) {
      const iframe = document.createElement('iframe');
      iframe.className = 'embedded-video video-iframe';
      iframe.src = info.src;
      iframe.title = videoData.title || videoData.label || 'Embedded video';
      iframe.loading = 'lazy';
      iframe.allow = 'accelerometer; autoplay; clipboard-write; encrypted-media; gyroscope; picture-in-picture; web-share';
      iframe.allowFullscreen = true;
      iframe.referrerPolicy = 'strict-origin-when-cross-origin';
      iframe.dataset.videoProvider = info.provider;
      if (info.id) iframe.dataset.videoId = info.id;
      frame.appendChild(iframe);
    } else {
      const error = document.createElement('div');
      error.className = 'broken-attachment video-error';
      error.textContent = 'Blocked or invalid video target';
      frame.appendChild(error);
    }

    frame.appendChild(toggle);
    figure.append(sourceRow, frame);
    block.appendChild(figure);
  }

  function hydrateVideoPlayers(root = el.previewPane) {
    const blocks = [
      ...(root.matches?.('.live-block.is-video-block') ? [root] : []),
      ...root.querySelectorAll('.live-block.is-video-block'),
    ];
    blocks.forEach(block => {
      const playerElement = block.querySelector('[data-video-provider]');
      const provider = playerElement?.dataset.videoProvider || block.querySelector('.live-video-frame')?.dataset.videoProvider;
      if (!provider || provider === 'invalid') return;
      const tracker = createPlaybackTracker(block);

      if (provider === 'html5') {
        const video = block.querySelector('video.embedded-video');
        if (!video) return;
        video.addEventListener('timeupdate', () => tracker.observe(video.currentTime, video.duration, video.playbackRate));
        video.addEventListener('ended', () => {
          tracker.observe(video.currentTime, video.duration, video.playbackRate);
          tracker.finish({ naturalEnd: true });
        });
        return;
      }

      if (provider === 'youtube') {
        const host = block.querySelector('.youtube-player-host[data-video-provider="youtube"]');
        if (!host) return;
        loadYouTubeApi().then(YT => {
          if (!host.isConnected) return;
          let progressTimer = null;
          const stopProgress = () => {
            if (progressTimer !== null) clearInterval(progressTimer);
            progressTimer = null;
          };
          const sample = player => {
            try {
              tracker.observe(player.getCurrentTime(), player.getDuration(), player.getPlaybackRate?.() || 1);
            } catch (error) {
              console.debug('Could not sample YouTube playback', error);
            }
          };
          const playerVars = { playsinline: 1, rel: 0 };
          if (/^https?:$/i.test(location.protocol)) playerVars.origin = location.origin;

          const player = new YT.Player(host, {
            videoId: host.dataset.videoId,
            playerVars,
            events: {
              onReady: event => {
                const iframe = event.target.getIframe?.();
                if (iframe) {
                  iframe.classList.add('embedded-video', 'video-iframe');
                  iframe.title = host.dataset.videoTitle || 'Embedded video';
                  iframe.referrerPolicy = 'strict-origin-when-cross-origin';
                }
              },
              onStateChange: event => {
                if (event.data === YT.PlayerState.PLAYING) {
                  sample(event.target);
                  if (progressTimer === null) progressTimer = setInterval(() => sample(event.target), 400);
                  return;
                }

                sample(event.target);
                stopProgress();
                if (event.data === YT.PlayerState.ENDED) tracker.finish({ naturalEnd: true });
              },
            },
          });
          registerMediaPlayer(block, () => {
            stopProgress();
            player.destroy?.();
          });
        }).catch(error => {
          console.warn('YouTube completion tracking unavailable:', error);
          const currentHost = block.querySelector('.youtube-player-host[data-video-provider="youtube"]');
          if (currentHost?.isConnected) {
            const fallback = document.createElement('iframe');
            fallback.className = 'embedded-video video-iframe';
            fallback.src = currentHost.dataset.videoSrc || '';
            fallback.title = currentHost.dataset.videoTitle || 'Embedded video';
            fallback.allow = 'accelerometer; autoplay; clipboard-write; encrypted-media; gyroscope; picture-in-picture; web-share';
            fallback.allowFullscreen = true;
            fallback.referrerPolicy = 'strict-origin-when-cross-origin';
            currentHost.replaceWith(fallback);
          }
          markVideoTrackingUnavailable(block, 'YouTube completion tracking could not initialise, but the fallback player remains available.');
        });
        return;
      }

      if (provider === 'vimeo') {
        const iframe = block.querySelector('iframe[data-video-provider="vimeo"]');
        if (!iframe) return;
        loadVimeoApi().then(Vimeo => {
          if (!iframe.isConnected) return;
          const player = new Vimeo.Player(iframe);
          player.on('timeupdate', data => tracker.observe(data.seconds, data.duration, 1));
          player.on('ended', data => {
            if (data) tracker.observe(data.seconds, data.duration, 1);
            tracker.finish({ naturalEnd: true });
          });
          registerMediaPlayer(block, () => player.destroy?.());
        }).catch(error => {
          console.warn('Vimeo completion tracking unavailable:', error);
          markVideoTrackingUnavailable(block, 'Vimeo completion tracking is unavailable while its player API cannot load.');
        });
        return;
      }

      if (provider === 'generic') {
        markVideoTrackingUnavailable(block, 'This provider can be embedded, but it does not expose a supported completion event.');
      }
    });
  }

  function createPlaybackTracker(block) {
    const naturalEndGraceSeconds = 1.5;
    let lastTime = 0;
    let lastObservedAt = performance.now();
    let watchedSeconds = 0;
    let duration = 0;

    return {
      observe(currentTime, reportedDuration, playbackRate = 1) {
        const current = Number(currentTime);
        const nextDuration = Number(reportedDuration);
        const rate = Math.max(0.25, Number(playbackRate) || 1);
        const observedAt = performance.now();
        if (Number.isFinite(nextDuration) && nextDuration > 0) duration = nextDuration;
        if (!Number.isFinite(current) || current < 0) return;

        const delta = current - lastTime;
        const wallSeconds = Math.max(0, (observedAt - lastObservedAt) / 1000);
        // Allow delayed samples (for example, a throttled background timer), but
        // still reject an instantaneous seek that jumps well ahead of real time.
        const maximumContinuousStep = Math.max(5, rate * (wallSeconds + 1.5));
        if (delta >= 0 && delta <= maximumContinuousStep) watchedSeconds += delta;
        lastTime = current;
        lastObservedAt = observedAt;
        if (duration > 0) watchedSeconds = Math.min(watchedSeconds, duration);
      },
      finish({ naturalEnd = false } = {}) {
        if (block.dataset.videoTask !== 'true' || block.classList.contains('is-video-complete')) return;
        const completedRatio = duration > 0 ? watchedSeconds / duration : 1;
        const watchedToNaturalEnd = naturalEnd
          && duration > 0
          && watchedSeconds >= Math.max(0, duration - naturalEndGraceSeconds);
        if (completedRatio >= 0.95 || watchedToNaturalEnd) {
          markVideoTaskComplete(block);
        } else {
          markVideoPlaybackIncomplete(block, completedRatio);
        }
      },
    };
  }

  function markVideoPlaybackIncomplete(block, completedRatio) {
    if (!block?.isConnected || block.dataset.videoTask !== 'true') return;
    const percentage = Math.max(0, Math.min(99, Math.round(completedRatio * 100)));
    const note = block.querySelector('.video-completion-note');
    if (note) note.textContent = `Locked · ${percentage}% played without skipping`;
    showToast('Video not checked — play it through without skipping', true);
  }

  function registerMediaPlayer(block, destroy) {
    state.mediaPlayers.push({ block, destroy });
  }

  function destroyMediaPlayers(root = null) {
    const remaining = [];
    for (const record of state.mediaPlayers) {
      if (root && record.block !== root && !root.contains?.(record.block)) {
        remaining.push(record);
        continue;
      }
      try { record.destroy?.(); } catch (error) { console.debug('Could not destroy media player', error); }
    }
    state.mediaPlayers = root ? remaining : [];
  }

  function markVideoTrackingUnavailable(block, message) {
    if (!block?.dataset.videoTask || block.classList.contains('is-video-complete')) return;
    const note = block.querySelector('.video-completion-note');
    if (note) note.textContent = 'Locked · completion tracking unavailable';
    const status = block.querySelector('.video-completion-status');
    if (status) status.title = message;
  }

  function markVideoTaskComplete(block) {
    if (!block?.isConnected || block.dataset.videoTask !== 'true') return;
    const source = block.dataset.source || '';
    if (!/^(\s*[-+*]\s+)\[>\]/.test(source)) return;

    destroyMediaPlayers(block);
    block.dataset.source = source.replace(/^(\s*[-+*]\s+)\[>\]/, '$1[x]');
    if (state.activeLiveBlock === block) state.activeLiveBlock = null;
    renderLiveBlock(block);
    hydrateAttachments(block).then(() => hydrateVideoPlayers(block));
    persistRenderedDocument();
    showToast('Video completed — task checked');
  }

  function loadExternalScript(url, readyTest) {
    if (readyTest?.()) return Promise.resolve();
    if (externalScriptPromises.has(url)) return externalScriptPromises.get(url);

    const promise = new Promise((resolve, reject) => {
      const existing = [...document.scripts].find(script => script.src === url);
      const script = existing || document.createElement('script');
      let settled = false;
      const finish = () => {
        if (settled) return;
        settled = true;
        readyTest?.() ? resolve() : reject(new Error(`Loaded ${url}, but its API was not available.`));
      };
      script.addEventListener('load', finish, { once: true });
      script.addEventListener('error', () => reject(new Error(`Could not load ${url}`)), { once: true });
      if (!existing) {
        script.src = url;
        script.async = true;
        document.head.appendChild(script);
      }
      setTimeout(() => {
        if (readyTest?.()) resolve();
        else if (!settled) reject(new Error(`Timed out loading ${url}`));
      }, 15000);
    });

    externalScriptPromises.set(url, promise);
    return promise;
  }

  function loadYouTubeApi() {
    if (window.YT?.Player) return Promise.resolve(window.YT);
    const key = 'youtube-iframe-api';
    if (externalScriptPromises.has(key)) return externalScriptPromises.get(key);

    const promise = new Promise((resolve, reject) => {
      const previous = window.onYouTubeIframeAPIReady;
      const timeout = setTimeout(() => reject(new Error('Timed out loading the YouTube IFrame API.')), 15000);
      window.onYouTubeIframeAPIReady = () => {
        try { previous?.(); } catch (error) { console.debug(error); }
        clearTimeout(timeout);
        if (window.YT?.Player) resolve(window.YT);
        else reject(new Error('YouTube IFrame API did not initialise.'));
      };

      const existing = [...document.scripts].some(script => script.src.includes('youtube.com/iframe_api'));
      if (!existing) {
        const script = document.createElement('script');
        script.src = 'https://www.youtube.com/iframe_api';
        script.async = true;
        script.onerror = () => {
          clearTimeout(timeout);
          reject(new Error('Could not load the YouTube IFrame API.'));
        };
        document.head.appendChild(script);
      }
    });

    externalScriptPromises.set(key, promise);
    return promise;
  }

  async function loadVimeoApi() {
    await loadExternalScript('https://player.vimeo.com/api/player.js', () => Boolean(window.Vimeo?.Player));
    return window.Vimeo;
  }

  function parseStandaloneImage(source) {
    const match = source.match(/^!\[([^\]]*)\]\(([^)\s]+)(?:\s+"([^"]*)")?\)$/);
    if (!match) return null;
    return { alt: match[1], url: match[2], title: match[3] || '' };
  }

  function createRenderedImage(alt, url, title = '') {
    const image = document.createElement('img');
    image.alt = alt;
    if (title) image.title = title;
    if (isAttachmentUrl(url)) {
      image.dataset.attachmentId = attachmentIdFromUrl(url);
    } else {
      const safe = safeUrl(url, true);
      if (safe) image.src = safe;
      else image.classList.add('blocked-image');
    }
    return image;
  }

  function renderLiveImageBlock(block, source, imageData) {
    block.classList.add('is-image-block');
    block.contentEditable = 'false';

    const figure = document.createElement('figure');
    figure.className = 'live-media-figure live-image-figure';

    const sourceRow = document.createElement('div');
    sourceRow.className = 'media-source-row image-source-row';
    const sourceEditor = document.createElement('span');
    sourceEditor.className = 'media-source-editor image-source-editor';
    sourceEditor.textContent = source;
    sourceEditor.contentEditable = 'false';
    sourceEditor.spellcheck = false;
    sourceRow.appendChild(sourceEditor);

    const toggle = document.createElement('button');
    toggle.type = 'button';
    toggle.className = 'media-source-toggle image-source-toggle';
    toggle.title = 'Edit image Markdown';
    toggle.setAttribute('aria-label', 'Edit image Markdown');
    toggle.dataset.sourceIgnore = 'true';

    const image = createRenderedImage(imageData.alt, imageData.url, imageData.title);
    image.contentEditable = 'false';
    image.dataset.sourceIgnore = 'true';

    const frame = document.createElement('div');
    frame.className = 'live-media-frame live-image-frame';
    frame.append(toggle, image);
    figure.append(sourceRow, frame);
    block.appendChild(figure);
  }

  function renderMarkdown(markdown) {
    const lines = markdown.replace(/\r\n?/g, '\n').split('\n');
    const html = [];
    let index = 0;

    while (index < lines.length) {
      const line = lines[index];

      if (/^\s*```/.test(line)) {
        const language = line.trim().slice(3).trim();
        const code = [];
        index += 1;
        while (index < lines.length && !/^\s*```/.test(lines[index])) {
          code.push(lines[index]);
          index += 1;
        }
        if (index < lines.length) index += 1;
        const className = language ? ` class="language-${escapeAttribute(language)}"` : '';
        html.push(`<pre><code${className}>${escapeHtml(code.join('\n'))}</code></pre>`);
        continue;
      }

      if (/^\s*$/.test(line)) {
        index += 1;
        continue;
      }

      const heading = line.match(/^(#{1,6})\s+(.+)$/);
      if (heading) {
        const level = heading[1].length;
        html.push(`<h${level}>${inlineMarkdown(heading[2])}</h${level}>`);
        index += 1;
        continue;
      }

      if (/^\s*((\*\s*){3,}|(-\s*){3,}|(_\s*){3,})\s*$/.test(line)) {
        html.push('<hr>');
        index += 1;
        continue;
      }

      if (/^\s*>/.test(line)) {
        const quoteLines = [];
        while (index < lines.length && /^\s*>/.test(lines[index])) {
          quoteLines.push(lines[index].replace(/^\s*>\s?/, ''));
          index += 1;
        }
        html.push(`<blockquote>${quoteLines.map(value => `<p>${inlineMarkdown(value)}</p>`).join('')}</blockquote>`);
        continue;
      }

      if (isTableStart(lines, index)) {
        const headers = splitTableRow(lines[index]);
        const aligns = splitTableRow(lines[index + 1]).map(cell => {
          const text = cell.trim();
          if (text.startsWith(':') && text.endsWith(':')) return 'center';
          if (text.endsWith(':')) return 'right';
          return 'left';
        });
        index += 2;
        const rows = [];
        while (index < lines.length && lines[index].includes('|') && lines[index].trim()) {
          rows.push(splitTableRow(lines[index]));
          index += 1;
        }
        const head = headers.map((cell, i) => `<th style="text-align:${aligns[i] || 'left'}">${inlineMarkdown(cell.trim())}</th>`).join('');
        const body = rows.map(row => `<tr>${headers.map((_, i) => `<td style="text-align:${aligns[i] || 'left'}">${inlineMarkdown((row[i] || '').trim())}</td>`).join('')}</tr>`).join('');
        html.push(`<table><thead><tr>${head}</tr></thead><tbody>${body}</tbody></table>`);
        continue;
      }

      const listMatch = line.match(/^\s*((?:[-+*])|(?:\d+\.))\s+(.+)$/);
      if (listMatch) {
        const ordered = /\d+\./.test(listMatch[1]);
        const tag = ordered ? 'ol' : 'ul';
        const items = [];
        while (index < lines.length) {
          const match = lines[index].match(/^\s*((?:[-+*])|(?:\d+\.))\s+(.+)$/);
          if (!match || /\d+\./.test(match[1]) !== ordered) break;
          const sourceLine = index;
          const task = match[2].match(/^\[([ xX])\]\s*(.*)$/);
          if (task) {
            const checked = task[1].toLowerCase() === 'x';
            items.push(`<li class="task-item"><input class="task-checkbox" type="checkbox" data-line="${sourceLine}" contenteditable="false" ${checked ? 'checked' : ''}>${inlineMarkdown(task[2])}</li>`);
          } else {
            items.push(`<li>${inlineMarkdown(match[2])}</li>`);
          }
          index += 1;
        }
        html.push(`<${tag}>${items.join('')}</${tag}>`);
        continue;
      }

      const paragraph = [line.trim()];
      index += 1;
      while (index < lines.length && lines[index].trim() && !isBlockStart(lines, index)) {
        paragraph.push(lines[index].trim());
        index += 1;
      }
      html.push(`<p>${inlineMarkdown(paragraph.join(' '))}</p>`);
    }

    return html.join('\n');
  }

  function htmlToMarkdown(root) {
    const blocks = [...root.childNodes]
      .map(node => blockNodeToMarkdown(node, 0))
      .filter(value => value !== null);

    return blocks
      .join('\n\n')
      .replace(/[ \t]+\n/g, '\n')
      .replace(/\n{3,}/g, '\n\n')
      .trim();
  }

  function blockNodeToMarkdown(node, depth = 0) {
    if (node.nodeType === Node.TEXT_NODE) {
      const text = normaliseEditableText(node.nodeValue || '').trim();
      return text || null;
    }
    if (node.nodeType !== Node.ELEMENT_NODE) return null;

    const tag = node.tagName.toLowerCase();
    if (/^h[1-6]$/.test(tag)) {
      return `${'#'.repeat(Number(tag.slice(1)))} ${inlineNodeToMarkdown(node).trim()}`;
    }
    if (tag === 'p' || tag === 'div') {
      const value = inlineNodeToMarkdown(node).trim();
      return value || '';
    }
    if (tag === 'pre') {
      const code = node.querySelector('code');
      const language = [...(code?.classList || [])]
        .find(name => name.startsWith('language-'))?.slice('language-'.length) || '';
      return `\`\`\`${language}\n${normaliseEditableText(code?.textContent ?? node.textContent ?? '')}\n\`\`\``;
    }
    if (tag === 'blockquote') {
      const inner = [...node.childNodes]
        .map(child => blockNodeToMarkdown(child, depth + 1))
        .filter(value => value !== null)
        .join('\n\n');
      return inner.split('\n').map(line => `> ${line}`.trimEnd()).join('\n');
    }
    if (tag === 'ul' || tag === 'ol') return listNodeToMarkdown(node, depth);
    if (tag === 'table') return tableNodeToMarkdown(node);
    if (tag === 'hr') return '---';
    if (tag === 'br') return '';
    return inlineNodeToMarkdown(node).trim() || null;
  }

  function listNodeToMarkdown(list, depth = 0) {
    const ordered = list.tagName.toLowerCase() === 'ol';
    const lines = [];
    let number = Number(list.getAttribute('start')) || 1;

    [...list.children].forEach(child => {
      if (child.tagName.toLowerCase() !== 'li') return;
      const nestedLists = [...child.children].filter(element => /^(ul|ol)$/i.test(element.tagName));
      const checkbox = [...child.children].find(element => element.matches?.('input.task-checkbox, input[type="checkbox"]'));
      const contentNodes = [...child.childNodes].filter(node => {
        if (node.nodeType !== Node.ELEMENT_NODE) return true;
        return !/^(ul|ol)$/i.test(node.tagName) && !node.matches?.('input[type="checkbox"]');
      });
      const text = contentNodes.map(inlineNodeToMarkdown).join('').trim();
      const marker = ordered ? `${number}.` : '-';
      const task = checkbox ? `[${checkbox.checked ? 'x' : ' '}] ` : '';
      lines.push(`${'  '.repeat(depth)}${marker} ${task}${text}`.trimEnd());
      nestedLists.forEach(nested => lines.push(listNodeToMarkdown(nested, depth + 1)));
      number += 1;
    });

    return lines.join('\n');
  }

  function tableNodeToMarkdown(table) {
    const rows = [...table.querySelectorAll('tr')];
    if (!rows.length) return '';
    const values = rows.map(row => [...row.children].map(cell => inlineNodeToMarkdown(cell).trim().replace(/\|/g, '\\|')));
    const width = Math.max(...values.map(row => row.length));
    const normaliseRow = row => Array.from({ length: width }, (_, index) => row[index] || '');
    const header = normaliseRow(values[0]);
    const body = values.slice(1).map(normaliseRow);
    return [
      `| ${header.join(' | ')} |`,
      `| ${header.map(() => '---').join(' | ')} |`,
      ...body.map(row => `| ${row.join(' | ')} |`),
    ].join('\n');
  }

  function inlineNodeToMarkdown(node) {
    if (node.nodeType === Node.TEXT_NODE) return normaliseEditableText(node.nodeValue || '');
    if (node.nodeType !== Node.ELEMENT_NODE) return '';

    const tag = node.tagName.toLowerCase();
    const inner = () => [...node.childNodes].map(inlineNodeToMarkdown).join('');

    if (tag === 'br') return '\n';
    if (tag === 'strong' || tag === 'b') return `**${inner()}**`;
    if (tag === 'em' || tag === 'i') return `*${inner()}*`;
    if (tag === 'del' || tag === 's' || tag === 'strike') return `~~${inner()}~~`;
    if (tag === 'code' && node.parentElement?.tagName.toLowerCase() !== 'pre') return `\`${normaliseEditableText(node.textContent || '')}\``;
    if (tag === 'img') {
      const alt = (node.getAttribute('alt') || 'image').replace(/([\[\]])/g, '\\$1');
      const attachmentId = node.dataset.attachmentId;
      const source = attachmentId ? `attachment://${attachmentId}` : (node.getAttribute('src') || '');
      return `![${alt}](${source})`;
    }
    if (tag === 'a') {
      const label = inner() || node.textContent || 'link';
      if (node.classList.contains('wiki-link')) {
        const target = node.dataset.noteName || label;
        return label === target ? `[[${target}]]` : `[[${target}|${label}]]`;
      }
      const attachmentId = node.dataset.attachmentId;
      const href = attachmentId ? `attachment://${attachmentId}` : (node.getAttribute('href') || '');
      return `[${label}](${href})`;
    }
    if (tag === 'input') return '';
    return inner();
  }

  function normaliseEditableText(value) {
    return String(value).replace(/\u00a0/g, ' ').replace(/\u200B/g, '');
  }

  function isBlockStart(lines, index) {
    const line = lines[index];
    return /^\s*```/.test(line) || /^(#{1,6})\s+/.test(line) || /^\s*>/.test(line) ||
      /^\s*((?:[-+*])|(?:\d+\.))\s+/.test(line) ||
      /^\s*((\*\s*){3,}|(-\s*){3,}|(_\s*){3,})\s*$/.test(line) ||
      isTableStart(lines, index);
  }

  function isTableStart(lines, index) {
    if (index + 1 >= lines.length || !lines[index].includes('|')) return false;
    const delimiter = lines[index + 1].trim();
    return /^\|?\s*:?-{3,}:?\s*(\|\s*:?-{3,}:?\s*)+\|?$/.test(delimiter);
  }

  function splitTableRow(line) {
    let value = line.trim();
    if (value.startsWith('|')) value = value.slice(1);
    if (value.endsWith('|')) value = value.slice(0, -1);
    return value.split(/(?<!\\)\|/).map(cell => cell.replace(/\\\|/g, '|'));
  }

  function inlineMarkdown(raw) {
    const tokens = [];
    let value = escapeHtml(raw);

    value = value.replace(/`([^`]+)`/g, (_, code) => tokenise(tokens, `<code>${code}</code>`));

    value = value.replace(/!\[([^\]]*)\]\(([^)\s]+)(?:\s+[&quot;]*([^&]*?)[&quot;]*)?\)/g, (_, alt, rawUrl, title) => {
      const url = decodeHtmlEntities(rawUrl);
      if (isAttachmentUrl(url)) {
        return tokenise(tokens, `<img data-attachment-id="${escapeAttribute(attachmentIdFromUrl(url))}" alt="${escapeAttribute(decodeHtmlEntities(alt))}">`);
      }
      const safe = safeUrl(url, true);
      if (!safe) return tokenise(tokens, `<span class="broken-attachment">Blocked image</span>`);
      const titleAttr = title ? ` title="${escapeAttribute(decodeHtmlEntities(title))}"` : '';
      return tokenise(tokens, `<img src="${escapeAttribute(safe)}" alt="${escapeAttribute(decodeHtmlEntities(alt))}"${titleAttr}>`);
    });

    value = value.replace(/\[([^\]]+)\]\(([^)\s]+)(?:\s+[&quot;]*([^&]*?)[&quot;]*)?\)/g, (_, label, rawUrl, title) => {
      const url = decodeHtmlEntities(rawUrl);
      const titleAttr = title ? ` title="${escapeAttribute(decodeHtmlEntities(title))}"` : '';
      if (isAttachmentUrl(url)) {
        return tokenise(tokens, `<a class="attachment-link" data-attachment-id="${escapeAttribute(attachmentIdFromUrl(url))}" href="#"${titleAttr}>${label}</a>`);
      }
      const safe = safeUrl(url, false);
      if (!safe) return label;
      return tokenise(tokens, `<a href="${escapeAttribute(safe)}" target="_blank" rel="noopener noreferrer"${titleAttr}>${label}</a>`);
    });

    value = value.replace(/\[\[([^\]|]+)(?:\|([^\]]+))?\]\]/g, (_, noteName, displayName) => {
      const name = decodeHtmlEntities(noteName.trim());
      const display = displayName || noteName;
      return tokenise(tokens, `<a href="#" class="wiki-link" data-note-name="${escapeAttribute(name)}">${display}</a>`);
    });

    value = value
      .replace(/\*\*([^*]+)\*\*/g, '<strong>$1</strong>')
      .replace(/__([^_]+)__/g, '<strong>$1</strong>')
      .replace(/~~([^~]+)~~/g, '<del>$1</del>')
      .replace(/(^|[^*])\*([^*\n]+)\*/g, '$1<em>$2</em>')
      .replace(/(^|[^_])_([^_\n]+)_/g, '$1<em>$2</em>')
      .replace(/  $/g, '<br>');

    return restoreTokens(value, tokens);
  }

  function tokenise(tokens, html) {
    const id = tokens.push(html) - 1;
    return `\u0000TOKEN${id}\u0000`;
  }

  function restoreTokens(value, tokens) {
    return value.replace(/\u0000TOKEN(\d+)\u0000/g, (_, id) => tokens[Number(id)] || '');
  }

  function escapeHtml(value) {
    return String(value)
      .replace(/&/g, '&amp;')
      .replace(/</g, '&lt;')
      .replace(/>/g, '&gt;')
      .replace(/"/g, '&quot;')
      .replace(/'/g, '&#039;');
  }

  function escapeAttribute(value) {
    return escapeHtml(String(value)).replace(/`/g, '&#096;');
  }

  function decodeHtmlEntities(value) {
    const textarea = document.createElement('textarea');
    textarea.innerHTML = value;
    return textarea.value;
  }

  function safeUrl(url, isImage) {
    const trimmed = String(url).trim();
    if (!trimmed) return '';
    const lower = trimmed.toLowerCase();
    if (lower.startsWith('javascript:') || lower.startsWith('vbscript:')) return '';
    if (lower.startsWith('data:')) {
      return isImage && /^data:image\/(png|jpe?g|gif|webp|svg\+xml);/i.test(trimmed) ? trimmed : '';
    }
    return trimmed;
  }

  function isAttachmentUrl(url) {
    return String(url).toLowerCase().startsWith('attachment://');
  }

  function attachmentIdFromUrl(url) {
    return String(url).slice('attachment://'.length).split(/[?#]/)[0];
  }

  async function hydrateAttachments(root = el.previewPane) {
    const elements = [...root.querySelectorAll('[data-attachment-id]')];
    await Promise.all(elements.map(async element => {
      const record = await getRecord(ATTACHMENT_STORE, element.dataset.attachmentId);
      if (!record?.blob) {
        element.replaceWith(makeBrokenAttachment(element));
        return;
      }
      const objectUrl = URL.createObjectURL(record.blob);
      state.previewUrls.push(objectUrl);
      if (element.tagName === 'IMG' || element.tagName === 'VIDEO') {
        element.src = objectUrl;
        element.title = record.name;
      } else {
        element.href = objectUrl;
        element.download = record.name;
        element.title = `${record.name} (${formatBytes(record.size)})`;
      }
    }));
  }

  function makeBrokenAttachment(element) {
    const span = document.createElement('span');
    span.className = 'broken-attachment';
    span.textContent = element.tagName === 'IMG' ? 'Missing image' : element.tagName === 'VIDEO' ? 'Missing video' : element.textContent || 'Missing attachment';
    return span;
  }

  function revokePreviewUrls() {
    state.previewUrls.forEach(url => URL.revokeObjectURL(url));
    state.previewUrls = [];
  }

  function hydrateWikiLinks(root = el.previewPane) {
    root.querySelectorAll('.wiki-link').forEach(link => {
      link.title = 'Wiki links are rendered as document-local Markdown in this standalone editor.';
    });
  }


  async function handlePreviewClick(event) {
    const mediaToggle = event.target.closest('.media-source-toggle');
    if (mediaToggle) {
      event.preventDefault();
      if (state.editorMode !== 'edit') return;
      const block = mediaToggle.closest('.live-block.is-image-block, .live-block.is-video-block');
      if (block) toggleMediaSourceEditor(block);
      return;
    }

    const wiki = event.target.closest('.wiki-link');
    if (wiki) {
      event.preventDefault();
      return;
    }

    const link = event.target.closest('a');
    if (link && state.editorMode === 'edit' && !(event.ctrlKey || event.metaKey)) {
      event.preventDefault();
    }
  }


  function handlePreviewChange(event) {
    const checkbox = event.target.closest('.task-checkbox');
    if (!checkbox) return;
    const taskInteractionAllowed = state.editorMode === 'edit' || state.interactiveTasks;
    if (!taskInteractionAllowed) return;
    const block = checkbox.closest('.live-block');
    if (!block || block.dataset.videoTask === 'true') return;

    const mark = checkbox.checked ? 'x' : ' ';
    block.dataset.source = (block.dataset.source || '').replace(
      /^(\s*[-+*]\s+\[)[ xX](\])/,
      (_, before, after) => `${before}${mark}${after}`
    );
    persistRenderedDocument();
  }


  function handleDocumentPointerDown(event) {
    if (state.editorMode !== 'edit' || state.view !== 'live' || !state.activeLiveBlock) return;
    if (state.activeLiveBlock.contains(event.target)) return;
    if (el.formatToolbar.contains(event.target)) return;
    commitActiveLiveBlock();
  }


  function handleLivePointerDown(event) {
    if (state.editorMode !== 'edit') return;

    if (state.view !== 'live') return;
    if (event.target.closest('input, button')) return;
    if (event.target.closest('.live-media-figure')) return;
    if ((event.ctrlKey || event.metaKey) && event.target.closest('a')) return;

    const block = event.target.closest('.live-block');
    if (!block || block === state.activeLiveBlock) return;

    event.preventDefault();
    activateLiveBlock(block);
    placeCaretFromPoint(block, event.clientX, event.clientY);
  }

  function handleLiveFocusOut() {
    if (state.editorMode !== 'edit') return;

    if (state.suppressLiveFocusOut > 0) return;
    const expectedBlock = state.activeLiveBlock;
    setTimeout(() => {
      const block = state.activeLiveBlock;
      if (!block || block !== expectedBlock) return;
      if (block.contains(document.activeElement)) return;
      if (el.formatToolbar.contains(document.activeElement)) return;
      const selection = window.getSelection();
      if (selection?.rangeCount && block.contains(selection.getRangeAt(0).commonAncestorContainer)) return;
      commitActiveLiveBlock();
    }, 0);
  }

  function activateLiveBlock(block, caretOffset = null, caretEnd = caretOffset) {
    if (!block || state.view !== 'live' || isMediaBlock(block)) return;
    if (state.activeLiveBlock && state.activeLiveBlock !== block) commitActiveLiveBlock();

    state.activeLiveBlock = block;
    block.classList.remove('is-rendered', 'is-empty');
    block.classList.add('is-active');

    if (block.dataset.liveMode === 'raw') {
      block.classList.add('is-raw-active');
      block.contentEditable = 'true';
      block.textContent = block.dataset.source || '';
    } else {
      block.classList.remove('is-raw-active');
      block.contentEditable = 'true';
      if ((block.dataset.source || '') === '' && !block.querySelector('.live-paragraph')) {
        const paragraph = document.createElement('p');
        paragraph.className = 'live-paragraph';
        paragraph.appendChild(document.createTextNode(''));
        block.replaceChildren(paragraph);
      }
    }

    const sourceLength = liveBlockText(block).length;
    const start = caretOffset === null ? sourceLength : caretOffset;
    const end = caretEnd === null ? start : caretEnd;
    revealMarkdownForOffset(block, start, end);
    setCaretOffset(block, start, end);
    block.focus({ preventScroll: true });
    rememberLiveSelection();
  }

  function toggleMediaSourceEditor(block, selectionStart = null, selectionEnd = selectionStart) {
    if (!block) return;
    if (state.activeLiveBlock === block && selectionStart === null) {
      commitActiveLiveBlock();
      return;
    }

    if (state.activeLiveBlock && state.activeLiveBlock !== block) commitActiveLiveBlock();
    state.activeLiveBlock = block;
    block.classList.add('is-active', 'is-media-source-open');
    const editor = block.querySelector('.media-source-editor');
    if (!editor) return;
    editor.contentEditable = 'true';
    editor.focus({ preventScroll: true });

    const sourceLength = normaliseEditableText(editor.textContent || '').length;
    const start = selectionStart === null ? sourceLength : Math.max(0, Math.min(sourceLength, selectionStart));
    const end = selectionEnd === null ? start : Math.max(start, Math.min(sourceLength, selectionEnd));
    setCaretOffset(editor, start, end);
    rememberLiveSelection();
  }

  function commitActiveLiveBlock(options = {}) {
    const block = state.activeLiveBlock;
    if (!block) return;

    const source = liveBlockText(block);
    block.dataset.source = source;
    state.activeLiveBlock = null;
    state.liveRange = null;
    destroyMediaPlayers(block);

    if (isMediaBlock(block) && source.trim() === '') {
      const previous = block.previousElementSibling;
      const next = block.nextElementSibling;
      block.remove();
      const trailing = ensureTrailingLiveBlankBlock();
      if (options.sync !== false) syncLiveEditorToNote();
      const target = next?.isConnected ? next : previous?.isConnected ? previous : trailing;
      if (options.activateAfterDelete !== false && target) {
        const caret = target === previous ? (target.dataset.source || '').length : 0;
        activateLiveBlock(target, caret);
      }
      return;
    }

    renderLiveBlock(block);
    hydrateAttachments(block).then(() => hydrateVideoPlayers(block));
    hydrateWikiLinks(block);
    if (options.sync !== false) syncLiveEditorToNote();
  }

  function liveBlockText(block) {
    if (!block) return '';
    if (isMediaBlock(block)) {
      return normaliseEditableText(block.querySelector('.media-source-editor')?.textContent ?? block.dataset.source ?? '')
        .replace(/\r\n?/g, '\n');
    }
    return normaliseEditableText(block.textContent || '').replace(/\r\n?/g, '\n');
  }

  function refreshActiveLiveBlock(block, source, start, end = start) {
    if (!block || isMediaBlock(block)) return;
    state.suppressLiveFocusOut += 1;
    block.dataset.source = source;
    state.activeLiveBlock = null;
    renderLiveBlock(block);

    // Newly typed media stays as editable Markdown until the line is committed.
    if (isMediaBlock(block)) {
      block.className = 'live-block is-rendered';
      block.dataset.liveMode = 'raw';
      block.dataset.source = source;
      block.innerHTML = renderMarkdown(source);
    }

    activateLiveBlock(block, start, end);
    setTimeout(() => {
      state.suppressLiveFocusOut = Math.max(0, state.suppressLiveFocusOut - 1);
    }, 0);
  }

  function revealMarkdownForOffset(block, start, end = start) {
    if (state.editorMode !== 'edit' || state.readOnly || state.view !== 'live') return;
    if (!block || block.dataset.liveMode === 'raw') return;
    const previouslyEditing = new Set(block.querySelectorAll('.md-element.is-editing'));
    block.querySelectorAll('.md-element.is-editing').forEach(element => element.classList.remove('is-editing'));

    block.querySelectorAll('.md-element').forEach(element => {
      const elementStart = Number(element.dataset.mdStart);
      const elementEnd = Number(element.dataset.mdEnd);
      const contentStart = Number(element.dataset.mdContentStart);
      const contentEnd = Number(element.dataset.mdContentEnd);
      const overlapsContent = end >= contentStart && start <= contentEnd;
      const remainsInsideVisibleElement = previouslyEditing.has(element) && end >= elementStart && start <= elementEnd;
      if (overlapsContent || remainsInsideVisibleElement) element.classList.add('is-editing');
    });
  }

  function normaliseStoredOutlineOpen(value) {
    return value !== 'false';
  }

  function applyOutlineVisibility(open, options = {}) {
    state.outlineOpen = Boolean(open);
    el.workspaceBody?.classList.toggle('outline-closed', !state.outlineOpen);
    el.documentOutline?.setAttribute('aria-hidden', String(!state.outlineOpen));
    el.outlineToggle?.setAttribute('aria-expanded', String(state.outlineOpen));
    el.outlineClose?.setAttribute('aria-expanded', String(state.outlineOpen));
    updateOutlineControlState();
    if (options.persist !== false) writeSetting('slate.outlineOpen', state.outlineOpen);
  }

  function updateOutlineControlState() {
    const complete = state.completionState.complete;
    const completionSuffix = complete ? ' · all tasks complete' : '';
    const gutterAction = state.outlineOpen ? 'Document outline is open' : 'Open document outline';
    const panelAction = 'Close document outline';

    if (el.outlineToggle) {
      el.outlineToggle.setAttribute('aria-label', `${gutterAction}${completionSuffix}`);
      el.outlineToggle.title = `${gutterAction}${completionSuffix}`;
    }
    if (el.outlineClose) {
      el.outlineClose.setAttribute('aria-label', `${panelAction}${completionSuffix}`);
      el.outlineClose.title = `${panelAction}${completionSuffix}`;
    }
  }

  function scheduleDocumentOutlineUpdate() {
    clearTimeout(state.outlineTimer);
    const markdown = state.view === 'source'
      ? el.markdownEditor.value
      : (selectedNote()?.content || '');
    state.outlineTimer = setTimeout(() => {
      state.outlineTimer = null;
      updateDocumentOutline(markdown);
    }, 40);
  }

  function refreshLiveBlockLineIndexes() {
    if (!el.previewPane) return;
    let lineIndex = 0;
    el.previewPane.querySelectorAll(':scope > .live-block').forEach(block => {
      block.dataset.lineIndex = String(lineIndex);
      lineIndex += String(block.dataset.source ?? '').split('\n').length;
    });
  }

  function parseDocumentOutline(markdown) {
    const lines = String(markdown ?? '').replace(/\r\n?/g, '\n').split('\n');
    const roots = [];
    const flat = [];
    const stack = [];
    let inFence = false;
    let total = 0;
    let checked = 0;

    lines.forEach((line, lineIndex) => {
      if (/^\s*```/.test(line)) {
        inFence = !inFence;
        return;
      }
      if (inFence) return;

      const headingMatch = line.match(/^(#{1,6})\s+(.+?)\s*$/);
      if (headingMatch) {
        const level = headingMatch[1].length;
        const rawTitle = headingMatch[2].replace(/\s+#+\s*$/, '');
        const node = {
          id: `slate-heading-${flat.length + 1}`,
          level,
          lineIndex,
          source: line,
          title: outlineHeadingText(rawTitle),
          directTotal: 0,
          directChecked: 0,
          total: 0,
          checked: 0,
          children: [],
        };

        while (stack.length && stack.at(-1).level >= level) stack.pop();
        if (stack.length) stack.at(-1).children.push(node);
        else roots.push(node);
        stack.push(node);
        flat.push(node);
        return;
      }

      const taskMatch = line.match(/^\s*[-+*]\s+\[([ xX>])\](?:\s+|$)/);
      if (!taskMatch) return;
      const isChecked = taskMatch[1].toLowerCase() === 'x';
      total += 1;
      if (isChecked) checked += 1;
      const section = stack.at(-1);
      if (section) {
        section.directTotal += 1;
        if (isChecked) section.directChecked += 1;
      }
    });

    const aggregate = node => {
      let nodeTotal = node.directTotal;
      let nodeChecked = node.directChecked;
      node.children.forEach(child => {
        aggregate(child);
        nodeTotal += child.total;
        nodeChecked += child.checked;
      });
      node.total = nodeTotal;
      node.checked = nodeChecked;
    };
    roots.forEach(aggregate);

    return {
      roots,
      flat,
      completion: {
        hasCheckboxes: total > 0,
        total,
        checked,
        unchecked: total - checked,
        complete: total > 0 && checked === total,
      },
    };
  }

  function outlineHeadingText(source) {
    const text = String(source ?? '')
      .replace(/!\[([^\]]*)\]\([^)]*\)/g, '$1')
      .replace(/\[([^\]]+)\]\([^)]*\)/g, '$1')
      .replace(/\[\[([^\]|]+)\|([^\]]+)\]\]/g, '$2')
      .replace(/\[\[([^\]]+)\]\]/g, '$1')
      .replace(/<[^>]+>/g, '')
      .replace(/[*_~`]/g, '')
      .replace(/\\([\\`*_[\]{}()#+.!-])/g, '$1')
      .trim();
    return text || 'Untitled section';
  }

  function updateDocumentOutline(markdown = selectedNote()?.content || '') {
    if (!el.outlineNavigation) return;
    const result = parseDocumentOutline(markdown);
    const fragment = document.createDocumentFragment();

    if (!result.roots.length) {
      const empty = document.createElement('p');
      empty.className = 'outline-empty';
      empty.textContent = 'Add Markdown headings to create document navigation.';
      fragment.appendChild(empty);
    } else {
      fragment.appendChild(createOutlineList(result.roots));
    }
    el.outlineNavigation.replaceChildren(fragment);

    const headingCount = result.flat.length;
    if (!headingCount) el.outlineSummary.textContent = 'No headings';
    else if (!result.completion.hasCheckboxes) {
      el.outlineSummary.textContent = `${headingCount} heading${headingCount === 1 ? '' : 's'} · no tasks`;
    } else {
      el.outlineSummary.textContent = `${headingCount} heading${headingCount === 1 ? '' : 's'} · ${result.completion.checked}/${result.completion.total} checked`;
    }

    refreshLiveBlockLineIndexes();
    el.previewPane?.querySelectorAll('[data-outline-id]').forEach(element => {
      element.removeAttribute('data-outline-id');
      element.querySelector?.('[id^="slate-heading-"]')?.removeAttribute('id');
    });
    result.flat.forEach(node => {
      const block = el.previewPane?.querySelector(`:scope > .live-block[data-line-index="${node.lineIndex}"]`);
      if (!block) return;
      block.dataset.outlineId = node.id;
      const heading = block.querySelector('h1, h2, h3, h4, h5, h6');
      if (heading) heading.id = node.id;
    });

    applyDocumentCompletionState(result.completion);
  }

  function createOutlineList(nodes) {
    const list = document.createElement('ul');
    list.className = 'outline-list';
    nodes.forEach(node => {
      const item = document.createElement('li');
      item.className = 'outline-item';

      const button = document.createElement('button');
      button.type = 'button';
      button.className = 'outline-link';
      button.dataset.outlineId = node.id;
      button.dataset.lineIndex = String(node.lineIndex);
      button.title = node.title;

      const status = document.createElement('span');
      status.className = 'outline-status';
      status.setAttribute('aria-hidden', 'true');
      if (node.total === 0) {
        button.classList.add('no-status');
        button.setAttribute('aria-label', `${node.title}; no checkboxes`);
      } else if (node.checked === node.total) {
        status.classList.add('complete');
        button.setAttribute('aria-label', `${node.title}; all ${node.total} checkboxes checked`);
      } else {
        button.setAttribute('aria-label', `${node.title}; ${node.checked} of ${node.total} checkboxes checked`);
      }

      const title = document.createElement('span');
      title.className = 'outline-title';
      title.textContent = node.title;
      if (node.total > 0) button.appendChild(status);
      button.appendChild(title);
      item.appendChild(button);
      if (node.children.length) item.appendChild(createOutlineList(node.children));
      list.appendChild(item);
    });
    return list;
  }

  function handleOutlineNavigationClick(event) {
    const button = event.target.closest('.outline-link');
    if (!button) return;
    event.preventDefault();
    event.stopPropagation();
    const lineIndex = Number(button.dataset.lineIndex);
    if (!Number.isInteger(lineIndex)) return;

    if (state.editorMode === 'edit' && state.view === 'source') {
      const markdown = selectedNote()?.content || '';
      const lines = markdown.replace(/\r\n?/g, '\n').split('\n');
      const offset = lines.slice(0, lineIndex).reduce((sum, line) => sum + line.length + 1, 0);
      const end = offset + (lines[lineIndex]?.length || 0);
      el.markdownEditor.focus();
      el.markdownEditor.setSelectionRange(offset, end);
      const ratio = lines.length > 1 ? lineIndex / (lines.length - 1) : 0;
      el.markdownEditor.scrollTop = Math.max(0, ratio * el.markdownEditor.scrollHeight - el.markdownEditor.clientHeight * 0.25);
      return;
    }

    refreshLiveBlockLineIndexes();
    const block = el.previewPane.querySelector(`:scope > .live-block[data-line-index="${lineIndex}"]`);
    if (!block) return;
    scrollPreviewElementIntoView(block, { behavior: 'smooth', block: 'start' });
    block.classList.remove('outline-target');
    requestAnimationFrame(() => {
      block.classList.add('outline-target');
      setTimeout(() => block.classList.remove('outline-target'), 900);
    });
  }

  function scrollPreviewElementIntoView(target, options = {}) {
    const pane = el.previewPane;
    if (!pane || !target || !pane.contains(target)) return;

    const behavior = options.behavior === 'smooth' ? 'smooth' : 'auto';
    const alignment = options.block === 'start' ? 'start' : 'nearest';
    const paneRect = pane.getBoundingClientRect();
    const targetRect = target.getBoundingClientRect();
    let top = pane.scrollTop;

    if (alignment === 'start') {
      top += targetRect.top - paneRect.top;
    } else if (targetRect.top < paneRect.top) {
      top += targetRect.top - paneRect.top;
    } else if (targetRect.bottom > paneRect.bottom) {
      top += targetRect.bottom - paneRect.bottom;
    } else {
      return;
    }

    pane.scrollTo({ top: Math.max(0, top), behavior });
  }

  function applyDocumentCompletionState(next) {
    const previous = state.completionState;
    const stateChanged = previous.hasCheckboxes !== next.hasCheckboxes
      || previous.total !== next.total
      || previous.checked !== next.checked
      || previous.complete !== next.complete;
    state.completionState = Object.freeze({ ...next });
    el.app?.setAttribute('data-document-complete', String(next.complete));
    updateOutlineControlState();
    if (!stateChanged) return;

    dispatchEditorEvent('slate:completion-change', { ...state.completionState });

    if (next.complete && !previous.complete) {
      dispatchEditorEvent('slate:document-complete', { ...state.completionState });
      state.completionCallbacks.forEach(callback => {
        try { callback({ ...state.completionState }); }
        catch (error) { console.error('Slate document-complete callback failed:', error); }
      });
    }
  }

  function normaliseStoredView(view) {
    return view === 'source' ? 'source' : 'live';
  }

  function normaliseWorkspaceMode(mode) {
    return mode === 'preview' || mode === 'source' ? mode : 'edit';
  }

  function getWorkspaceMode() {
    if (state.editorMode === 'view') return 'preview';
    return state.view === 'source' ? 'source' : 'edit';
  }

  function updateWorkspaceModeControls() {
    const workspaceMode = getWorkspaceMode();
    el.app.dataset.workspaceMode = workspaceMode;
    el.viewButtons.forEach(button => {
      const active = button.dataset.workspaceMode === workspaceMode;
      button.classList.toggle('active', active);
      button.setAttribute('aria-pressed', String(active));
      button.disabled = state.readOnly;
    });
  }

  async function applyWorkspaceMode(mode, options = {}) {
    mode = normaliseWorkspaceMode(mode);
    if (state.readOnly && mode !== 'preview' && !options.allowReadOnly) return getWorkspaceMode();
    if (mode === 'preview') {
      await applyEditorMode('view', options);
      return;
    }

    const targetView = mode === 'source' ? 'source' : 'live';
    if (state.editorMode === 'view') {
      state.view = targetView;
      writeSetting('slate.editorView', targetView);
      await applyEditorMode('edit', options);
    } else {
      applyView(targetView, options);
    }
  }

  async function applyReadOnly(readOnly) {
    const next = Boolean(readOnly);
    if (next === state.readOnly) return getWorkspaceMode();

    if (next) {
      const currentMode = getWorkspaceMode();
      if (currentMode !== 'preview') state.lastWritableWorkspaceMode = currentMode;
      state.readOnly = true;
      el.app.dataset.readOnly = 'true';
      el.formatToolbar.hidden = true;
      await applyWorkspaceMode('preview', { allowReadOnly: true });
    } else {
      state.readOnly = false;
      el.app.dataset.readOnly = 'false';
      el.formatToolbar.hidden = false;
      await applyWorkspaceMode(state.lastWritableWorkspaceMode || 'edit', { allowReadOnly: true });
    }

    updateWorkspaceModeControls();
    return getWorkspaceMode();
  }

  function applyView(view, options = {}) {
    view = normaliseStoredView(view);
    if (state.editorMode !== 'edit' && !options.force) return;

    if (state.view === 'live' && view === 'source') commitActiveLiveBlock();

    state.view = view;
    writeSetting('slate.editorView', view);
    el.editorGrid.className = `editor-grid ${view}-view`;
    updateWorkspaceModeControls();

    if (options.render === false) return;
    if (view === 'live') {
      clearTimeout(state.previewTimer);
      renderPreview().then(() => placeCaretAtEndIfEmpty(el.previewPane));
    } else {
      const note = selectedNote();
      if (note) el.markdownEditor.value = note.content || '';
      el.markdownEditor.readOnly = false;
    }
  }


  function normaliseStoredEditorMode(mode) {
    return mode === 'view' ? 'view' : 'edit';
  }

  async function applyEditorMode(mode, options = {}) {
    mode = normaliseStoredEditorMode(mode);
    if (state.editorMode === 'edit' && mode === 'view') commitActiveLiveBlock();

    state.editorMode = mode;
    writeSetting('slate.editorMode', mode);
    el.app.dataset.editorMode = mode;
    el.formatControls.hidden = mode === 'view';
    el.formatToolbar.classList.toggle('preview-mode', mode === 'view');
    el.markdownEditor.readOnly = mode === 'view';

    if (mode === 'view') {
      el.editorGrid.className = 'editor-grid live-view view-only';
      if (options.render !== false) await renderPreview();
    } else {
      applyView(state.view, { force: true, render: options.render !== false });
      applyPreviewInteractivity();
    }
    updateWorkspaceModeControls();
  }

  function applyPreviewInteractivity() {
    const editable = state.editorMode === 'edit' && state.view === 'live' && !state.readOnly;
    const taskInteractionAllowed = editable || state.interactiveTasks;
    el.previewPane.classList.toggle('live-editor', editable);
    el.previewPane.classList.toggle('view-only', !editable);
    el.previewPane.classList.toggle('tasks-interactive', taskInteractionAllowed);
    el.previewPane.setAttribute('aria-label', editable ? 'Markdown editor' : 'Rendered Markdown document');
    el.previewPane.setAttribute('contenteditable', 'false');

    el.previewPane.querySelectorAll('.task-checkbox').forEach(checkbox => {
      checkbox.disabled = !taskInteractionAllowed || checkbox.closest('.live-block')?.dataset.videoTask === 'true';
    });
    el.previewPane.querySelectorAll('.media-source-toggle').forEach(button => {
      button.hidden = !editable;
    });
    if (!editable) {
      state.activeLiveBlock = null;
      state.liveRange = null;
      el.previewPane.querySelectorAll('.md-element.is-editing').forEach(element => {
        element.classList.remove('is-editing');
      });
      el.previewPane.querySelectorAll('[contenteditable="true"]').forEach(element => {
        element.contentEditable = 'false';
      });
      el.previewPane.querySelectorAll('.live-block.is-active, .live-block.is-raw-active, .live-block.is-media-source-open').forEach(block => {
        block.classList.remove('is-active', 'is-raw-active', 'is-media-source-open');
        renderLiveBlock(block);
      });
    }
  }

  function handleLiveInput(event) {
    if (state.editorMode !== 'edit') return;

    const block = event.target.closest('.live-block.is-active');
    if (!block) return;

    if (isMediaBlock(block)) {
      block.dataset.source = liveBlockText(block);
      syncLiveEditorToNote();
      return;
    }

    const offsets = selectionOffsets(block);
    const source = liveBlockText(block);
    block.dataset.source = source;
    if (!event.isComposing) refreshActiveLiveBlock(block, source, offsets.start, offsets.end);
    syncLiveEditorToNote();
  }

  function persistRenderedDocument() {
    const note = selectedNote();
    if (!note) return;
    ensureTrailingLiveBlankBlock();
    const markdown = ensureTrailingBlankLine([...el.previewPane.querySelectorAll(':scope > .live-block')]
      .map(block => block.dataset.source || '')
      .join('\n'));
    note.content = markdown;
    note.updatedAt = Date.now();
    el.markdownEditor.value = markdown;
    scheduleSave(note);
    updateDocumentStats();
    refreshLiveBlockLineIndexes();
    dispatchDocumentChange();
  }

  function syncLiveEditorToNote() {
    if (state.editorMode !== 'edit' || state.view !== 'live') return;
    if (state.activeLiveBlock) {
      state.activeLiveBlock.dataset.source = liveBlockText(state.activeLiveBlock);
    }
    persistRenderedDocument();
  }


  function handleLiveKeydown(event) {
    if (state.editorMode !== 'edit') return;

    const block = event.target.closest('.live-block.is-active');
    if (!block) return;

    if (isMediaBlock(block)) {
      const source = liveBlockText(block);
      const offsets = selectionOffsets(block);
      const collapsed = offsets.start === offsets.end;
      const plainArrow = !event.shiftKey && !event.altKey && !event.metaKey && !event.ctrlKey;

      if (plainArrow && (event.key === 'ArrowUp' || event.key === 'ArrowDown')) {
        const direction = event.key === 'ArrowUp' ? -1 : 1;
        if (moveCaretAcrossLiveBlocks(block, direction, offsets.start, 'column')) event.preventDefault();
        return;
      }
      if (plainArrow && event.key === 'ArrowLeft' && collapsed && offsets.start <= 0) {
        if (moveCaretAcrossLiveBlocks(block, -1, 0, 'edge')) event.preventDefault();
        return;
      }
      if (plainArrow && event.key === 'ArrowRight' && collapsed && offsets.end >= source.length) {
        if (moveCaretAcrossLiveBlocks(block, 1, 0, 'edge')) event.preventDefault();
        return;
      }
      if (event.key === 'Escape' || event.key === 'Enter') {
        event.preventDefault();
        commitActiveLiveBlock();
      }
      return;
    }

    const key = event.key.toLowerCase();
    if ((event.ctrlKey || event.metaKey) && key === 'b') {
      event.preventDefault();
      applyFormatting('bold');
      return;
    }
    if ((event.ctrlKey || event.metaKey) && key === 'i') {
      event.preventDefault();
      applyFormatting('italic');
      return;
    }
    if ((event.ctrlKey || event.metaKey) && key === 'k') {
      event.preventDefault();
      applyFormatting('link');
      return;
    }
    if (event.key === 'Tab') {
      event.preventDefault();
      insertTextIntoActiveBlock('  ');
      return;
    }
    if (event.key === 'Escape') {
      event.preventDefault();
      commitActiveLiveBlock();
      return;
    }

    const source = liveBlockText(block);
    const offsets = selectionOffsets(block);

    if (!event.shiftKey && !event.altKey && !event.metaKey && !event.ctrlKey) {
      const collapsed = offsets.start === offsets.end;
      if (event.key === 'ArrowUp') {
        if (moveCaretAcrossLiveBlocks(block, -1, offsets.start, 'column')) event.preventDefault();
        return;
      }
      if (event.key === 'ArrowDown') {
        if (moveCaretAcrossLiveBlocks(block, 1, offsets.start, 'column')) event.preventDefault();
        return;
      }
      if (event.key === 'ArrowLeft' && collapsed && offsets.start <= 0) {
        if (moveCaretAcrossLiveBlocks(block, -1, 0, 'edge')) event.preventDefault();
        return;
      }
      if (event.key === 'ArrowRight' && collapsed && offsets.end >= source.length) {
        if (moveCaretAcrossLiveBlocks(block, 1, 0, 'edge')) event.preventDefault();
        return;
      }
    }

    if (event.key === 'Enter') {
      event.preventDefault();
      if (event.shiftKey || source.includes('\n')) {
        insertTextIntoActiveBlock('\n');
      } else {
        splitActiveLiveBlock(block, source, offsets);
      }
      return;
    }

    if (event.key === 'Backspace' && offsets.start === 0 && offsets.end === 0) {
      const previous = block.previousElementSibling;
      if (previous?.classList.contains('live-block')) {
        event.preventDefault();
        mergeLiveBlocks(previous, block, (previous.dataset.source || '').length);
      }
      return;
    }

    if (event.key === 'Delete' && offsets.start === source.length && offsets.end === source.length) {
      const next = block.nextElementSibling;
      if (next?.classList.contains('live-block')) {
        event.preventDefault();
        mergeLiveBlocks(block, next, source.length);
      }
    }
  }

  function adjacentEditableLiveBlock(block, direction) {
    let candidate = direction < 0 ? block?.previousElementSibling : block?.nextElementSibling;
    while (candidate) {
      if (candidate.classList?.contains('live-block') && !isMediaBlock(candidate)) return candidate;
      candidate = direction < 0 ? candidate.previousElementSibling : candidate.nextElementSibling;
    }
    return null;
  }

  function moveCaretAcrossLiveBlocks(block, direction, column = 0, mode = 'column') {
    const target = adjacentEditableLiveBlock(block, direction);
    if (!target) return false;

    commitActiveLiveBlock({ activateAfterDelete: false });
    const targetLength = String(target.dataset.source || '').length;
    let caretOffset;
    if (mode === 'edge') caretOffset = direction < 0 ? targetLength : 0;
    else caretOffset = Math.min(Math.max(0, column), targetLength);
    activateLiveBlock(target, caretOffset);
    scrollPreviewElementIntoView(target, { block: 'nearest' });
    return true;
  }

  function splitActiveLiveBlock(block, source, offsets) {
    const before = source.slice(0, offsets.start);
    const after = source.slice(offsets.end);
    const prefix = markdownContinuationPrefix(before);

    block.dataset.source = before;
    state.activeLiveBlock = null;
    renderLiveBlock(block);
    hydrateAttachments(block).then(() => hydrateVideoPlayers(block));
    hydrateWikiLinks(block);

    const nextBlock = createLiveBlock(prefix + after);
    block.after(nextBlock);
    syncLiveEditorToNote();
    activateLiveBlock(nextBlock, prefix.length);
  }

  function markdownContinuationPrefix(before) {
    const task = before.match(/^(\s*[-+*]\s+)\[[ xX]\]\s+/);
    if (task) return `${task[1]}[ ] `;

    const unordered = before.match(/^(\s*[-+*]\s+)/);
    if (unordered) return unordered[1];

    const ordered = before.match(/^(\s*)(\d+)\.\s+/);
    if (ordered) return `${ordered[1]}${Number(ordered[2]) + 1}. `;

    const quote = before.match(/^(\s*>\s?)/);
    return quote ? quote[1] : '';
  }

  function mergeLiveBlocks(first, second, caretOffset) {
    if (state.activeLiveBlock === first) first.dataset.source = liveBlockText(first);
    if (state.activeLiveBlock === second) second.dataset.source = liveBlockText(second);
    const merged = (first.dataset.source || '') + (second.dataset.source || '');

    state.activeLiveBlock = null;
    first.dataset.source = merged;
    second.remove();
    renderLiveBlock(first);
    syncLiveEditorToNote();
    activateLiveBlock(first, caretOffset);
  }

  function selectionOffsets(block) {
    const root = isMediaBlock(block)
      ? block.querySelector('.media-source-editor')
      : block;
    const fallback = liveBlockText(block).length;
    const selection = window.getSelection();
    if (!root || !selection?.rangeCount) return { start: fallback, end: fallback };
    const range = selection.getRangeAt(0);
    if (!root.contains(range.commonAncestorContainer)) return { start: fallback, end: fallback };

    return {
      start: domPositionToTextOffset(root, range.startContainer, range.startOffset),
      end: domPositionToTextOffset(root, range.endContainer, range.endOffset),
    };
  }

  function domPositionToTextOffset(root, targetNode, targetOffset) {
    let total = 0;
    let found = false;

    function sourceLength(value) {
      return normaliseEditableText(value || '').length;
    }

    function visit(node) {
      if (found) return;
      if (node === targetNode) {
        if (node.nodeType === Node.TEXT_NODE) {
          total += sourceLength((node.nodeValue || '').slice(0, targetOffset));
        } else {
          const children = [...node.childNodes];
          for (let index = 0; index < Math.min(targetOffset, children.length); index += 1) {
            total += sourceLength(children[index].textContent || '');
          }
        }
        found = true;
        return;
      }

      if (node.nodeType === Node.TEXT_NODE) {
        total += sourceLength(node.nodeValue || '');
        return;
      }
      node.childNodes.forEach(visit);
    }

    visit(root);
    return total;
  }

  function setCaretOffset(block, start, end = start) {
    const root = isMediaBlock(block)
      ? block.querySelector('.media-source-editor')
      : block;
    if (!root) return;

    if (!isMediaBlock(block)) revealMarkdownForOffset(block, start, end);

    const nodes = [];
    const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT);
    while (walker.nextNode()) nodes.push(walker.currentNode);
    if (!nodes.length) {
      const textNode = document.createTextNode('');
      root.appendChild(textNode);
      nodes.push(textNode);
    }

    function rawOffsetForSourceOffset(value, sourceOffset) {
      let consumed = 0;
      if (sourceOffset <= 0 && value.startsWith('\u200B')) return value.length;
      for (let rawOffset = 0; rawOffset < value.length; rawOffset += 1) {
        if (value[rawOffset] !== '\u200B') consumed += 1;
        if (consumed >= sourceOffset) return rawOffset + 1;
      }
      return value.length;
    }

    function locate(offset) {
      let remaining = Math.max(0, offset);
      for (let index = 0; index < nodes.length; index += 1) {
        const node = nodes[index];
        const rawValue = node.nodeValue || '';
        const length = normaliseEditableText(rawValue).length;
        const isLast = index === nodes.length - 1;
        if (remaining < length) {
          return { node, offset: rawOffsetForSourceOffset(rawValue, remaining) };
        }
        if (remaining === length && isLast) {
          return { node, offset: rawOffsetForSourceOffset(rawValue, remaining) };
        }
        remaining -= length;
      }
      const node = nodes[nodes.length - 1];
      return { node, offset: node.nodeValue?.length || 0 };
    }

    const startPoint = locate(start);
    const endPoint = locate(end);
    const range = document.createRange();
    range.setStart(startPoint.node, startPoint.offset);
    range.setEnd(endPoint.node, endPoint.offset);
    const selection = window.getSelection();
    selection.removeAllRanges();
    selection.addRange(range);
  }

  function placeCaretFromPoint(block, x, y) {
    let range = null;
    const position = document.caretPositionFromPoint?.(x, y);
    if (position && block.contains(position.offsetNode)) {
      range = document.createRange();
      range.setStart(position.offsetNode, position.offset);
      range.collapse(true);
    } else {
      const legacyRange = document.caretRangeFromPoint?.(x, y);
      if (legacyRange && block.contains(legacyRange.startContainer)) range = legacyRange;
    }

    if (!range) return;
    const selection = window.getSelection();
    selection.removeAllRanges();
    selection.addRange(range);
    const offsets = selectionOffsets(block);
    revealMarkdownForOffset(block, offsets.start, offsets.end);
    setCaretOffset(block, offsets.start, offsets.end);
    rememberLiveSelection();
  }

  function insertTextIntoActiveBlock(text) {
    const block = state.activeLiveBlock;
    if (!block || isMediaBlock(block)) return;
    const source = liveBlockText(block);
    const offsets = selectionOffsets(block);
    const updated = source.slice(0, offsets.start) + text + source.slice(offsets.end);
    refreshActiveLiveBlock(block, updated, offsets.start + text.length);
    syncLiveEditorToNote();
  }

  function placeCaretAtEndIfEmpty(target) {
    if (!target) return;
    const blocks = [...target.querySelectorAll(':scope > .live-block')];
    if (!blocks.length || blocks.some(block => (block.dataset.source || '').trim())) return;
    activateLiveBlock(blocks[0], 0);
  }

  function applyStoredTheme(requestedTheme = 'auto') {
    const theme = requestedTheme === 'dark' || requestedTheme === 'light'
      ? requestedTheme
      : (matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light');
    mountHost.dataset.theme = theme;
  }



  function updateDocumentStats() {
    const text = el.markdownEditor.value || '';
    const words = text.trim() ? text.trim().split(/\s+/).length : 0;
    el.documentStats.textContent = `${words} word${words === 1 ? '' : 's'} · ${text.length} character${text.length === 1 ? '' : 's'}`;
  }

  function handleEditorKeydown(event) {
    if (state.editorMode !== 'edit') return;

    if (event.key === 'Tab') {
      event.preventDefault();
      replaceSelection('  ', '', '');
      return;
    }
    if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === 'b') {
      event.preventDefault();
      applyFormatting('bold');
    } else if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === 'i') {
      event.preventDefault();
      applyFormatting('italic');
    } else if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === 'k') {
      event.preventDefault();
      applyFormatting('link');
    }
  }

  function handleGlobalKeydown(event) {
    if (!(event.ctrlKey || event.metaKey)) return;
    const key = event.key.toLowerCase();
    if (key === 's') {
      event.preventDefault();
      flushPendingSave();
      showToast('Saved locally');
    } else if (key === 'e') {
      event.preventDefault();
      if (state.readOnly) return;
      applyWorkspaceMode(state.editorMode === 'view' ? (state.view === 'source' ? 'source' : 'edit') : 'preview');
    } else if (key === 'o' && event.shiftKey) {
      event.preventDefault();
      applyOutlineVisibility(!state.outlineOpen);
    }
  }


  function applyFormatting(kind) {
    if (state.editorMode !== 'edit') return;

    if (!selectedNote()) return;
    if (kind === 'video' || kind === 'video-task') {
      insertVideoTemplate(kind === 'video-task');
      return;
    }
    if (state.view === 'live') {
      applyLiveFormatting(kind);
      return;
    }

    const selections = {
      bold: ['**', '**', 'bold text'],
      italic: ['*', '*', 'italic text'],
      strike: ['~~', '~~', 'struck text'],
      code: ['`', '`', 'code'],
      link: ['[', '](https://example.com)', 'link text'],
      wikilink: ['[[', ']]', 'Note Name'],
    };
    if (selections[kind]) {
      replaceSelection(...selections[kind]);
      return;
    }

    if (kind === 'heading') prefixSelectedLines('## ');
    if (kind === 'quote') prefixSelectedLines('> ');
    if (kind === 'task') prefixSelectedLines('- [ ] ');
    if (kind === 'codeblock') replaceSelection('```\n', '\n```', 'code');
  }

  function insertVideoTemplate(tracked = false) {
    const snippet = `${tracked ? '- [>] ' : ''}!video[title](url)`;
    const urlStart = snippet.lastIndexOf('(url)') + 1;
    const urlEnd = urlStart + 3;

    if (state.view === 'source') {
      const editor = el.markdownEditor;
      const start = editor.selectionStart;
      const end = editor.selectionEnd;
      const before = editor.value.slice(0, start);
      const after = editor.value.slice(end);
      const prefix = before && !before.endsWith('\n') ? '\n' : '';
      const suffix = after && !after.startsWith('\n') ? '\n' : '';
      const inserted = `${prefix}${snippet}${suffix}`;
      editor.setRangeText(inserted, start, end, 'end');
      const selectionStart = start + prefix.length + urlStart;
      editor.value = ensureTrailingBlankLine(editor.value);
      editor.setSelectionRange(selectionStart, selectionStart + 3);
      editor.focus();
      editor.dispatchEvent(new Event('input', { bubbles: true }));
      return;
    }

    let active = state.activeLiveBlock;
    if (isMediaBlock(active)) {
      commitActiveLiveBlock({ sync: false, activateAfterDelete: false });
      active = null;
    }

    if (!active) {
      active = el.previewPane.querySelector(':scope > .live-block:last-child');
      if (!active) {
        active = createLiveBlock('');
        el.previewPane.appendChild(active);
      }
      if (isMediaBlock(active)) {
        const blank = createLiveBlock('');
        active.after(blank);
        active = blank;
      }
      activateLiveBlock(active, (active.dataset.source || '').length);
    }

    const source = liveBlockText(active);
    const offsets = selectionOffsets(active);
    const before = source.slice(0, offsets.start);
    const after = source.slice(offsets.end);
    let mediaBlock;

    state.activeLiveBlock = null;
    state.liveRange = null;
    destroyMediaPlayers(active);

    if (!before) {
      active.dataset.source = snippet;
      renderLiveBlock(active);
      mediaBlock = active;
      if (after) mediaBlock.after(createLiveBlock(after));
    } else {
      active.dataset.source = before;
      renderLiveBlock(active);
      mediaBlock = createLiveBlock(snippet);
      active.after(mediaBlock);
      if (after) mediaBlock.after(createLiveBlock(after));
    }

    ensureTrailingLiveBlankBlock();
    hydrateAttachments(mediaBlock).then(() => hydrateVideoPlayers(mediaBlock));
    syncLiveEditorToNote();
    toggleMediaSourceEditor(mediaBlock, urlStart, urlEnd);
  }

  function insertMarkdownBlock(snippet) {
    if (state.view === 'source') {
      const editor = el.markdownEditor;
      const start = editor.selectionStart;
      const prefix = start > 0 && !/\n\s*$/.test(editor.value.slice(0, start)) ? '\n\n' : '';
      const suffix = start < editor.value.length && !/^\s*\n/.test(editor.value.slice(start)) ? '\n\n' : '';
      editor.setRangeText(prefix + snippet + suffix, start, editor.selectionEnd, 'end');
      editor.focus();
      editor.dispatchEvent(new Event('input', { bubbles: true }));
      return;
    }

    if (state.activeLiveBlock) commitActiveLiveBlock({ sync: false });
    let reference = el.previewPane.querySelector(':scope > .live-block:last-child');
    if (!reference) {
      reference = createLiveBlock(snippet);
      el.previewPane.appendChild(reference);
    } else if ((reference.dataset.source || '') === '') {
      reference.dataset.source = snippet;
      renderLiveBlock(reference);
    } else {
      const block = createLiveBlock(snippet);
      reference.after(block);
      reference = block;
    }
    const trailing = createLiveBlock('');
    reference.after(trailing);
    hydrateAttachments(reference).then(() => hydrateVideoPlayers(reference));
    syncLiveEditorToNote();
    activateLiveBlock(trailing, 0);
  }

  function applyLiveFormatting(kind) {
    let block = state.activeLiveBlock;
    if (isMediaBlock(block)) block = null;
    if (!block) {
      block = el.previewPane.querySelector(':scope > .live-block:last-child:not(.is-image-block):not(.is-video-block)');
      if (!block) {
        block = createLiveBlock('');
        el.previewPane.appendChild(block);
      }
      activateLiveBlock(block);
    }

    const wrappers = {
      bold: ['**', '**', 'bold text'],
      italic: ['*', '*', 'italic text'],
      strike: ['~~', '~~', 'struck text'],
      code: ['`', '`', 'code'],
      link: ['[', '](https://example.com)', 'link text'],
      wikilink: ['[[', ']]', 'Note Name'],
    };

    if (wrappers[kind]) {
      replaceActiveLiveSelection(...wrappers[kind]);
      return;
    }

    const source = liveBlockText(block);
    const offsets = selectionOffsets(block);
    let prefix = '';
    if (kind === 'heading') prefix = '## ';
    else if (kind === 'quote') prefix = '> ';
    else if (kind === 'task') prefix = '- [ ] ';

    if (prefix) {
      const updated = prefix + source;
      refreshActiveLiveBlock(block, updated, offsets.start + prefix.length, offsets.end + prefix.length);
      syncLiveEditorToNote();
      return;
    }

    if (kind === 'codeblock') {
      const updated = `\`\`\`\n${source || 'code'}\n\`\`\``;
      refreshActiveLiveBlock(block, updated, 4, Math.max(4, updated.length - 4));
      syncLiveEditorToNote();
    }
  }

  function replaceActiveLiveSelection(before, after, placeholder) {
    const block = state.activeLiveBlock;
    if (!block || isMediaBlock(block)) return;
    const source = liveBlockText(block);
    const offsets = selectionOffsets(block);
    const selected = source.slice(offsets.start, offsets.end) || placeholder;
    const replacement = `${before}${selected}${after}`;
    const updated = source.slice(0, offsets.start) + replacement + source.slice(offsets.end);
    refreshActiveLiveBlock(
      block,
      updated,
      offsets.start + before.length,
      offsets.start + before.length + selected.length
    );
    syncLiveEditorToNote();
  }

  function wrapLiveSelection(tagName, attributes, placeholder) {
    const selection = window.getSelection();
    if (!selection?.rangeCount) return;
    const range = selection.getRangeAt(0);
    const element = document.createElement(tagName);
    Object.entries(attributes).forEach(([name, value]) => element.setAttribute(name, value));

    if (range.collapsed) {
      element.textContent = placeholder;
      range.insertNode(element);
      range.selectNodeContents(element);
    } else {
      try {
        range.surroundContents(element);
      } catch {
        element.appendChild(range.extractContents());
        range.insertNode(element);
      }
      range.selectNodeContents(element);
    }

    selection.removeAllRanges();
    selection.addRange(range);
  }

  function insertLiveHtml(html) {
    el.previewPane.focus();
    restoreLiveSelection();
    document.execCommand('insertHTML', false, html);
  }

  function rememberLiveSelection() {
    if (state.editorMode !== 'edit' || state.readOnly || state.view !== 'live') return;
    const selection = window.getSelection();
    if (!selection?.rangeCount) return;
    const range = selection.getRangeAt(0);
    if (!el.previewPane.contains(range.commonAncestorContainer)) return;
    state.liveRange = range.cloneRange();

    const block = state.activeLiveBlock;
    if (block && !isMediaBlock(block) && block.contains(range.commonAncestorContainer)) {
      const offsets = selectionOffsets(block);
      revealMarkdownForOffset(block, offsets.start, offsets.end);
    }
  }

  function restoreLiveSelection() {
    if (!state.liveRange) return false;
    const selection = window.getSelection();
    try {
      selection.removeAllRanges();
      selection.addRange(state.liveRange);
      return true;
    } catch {
      state.liveRange = null;
      return false;
    }
  }

  function replaceSelection(before, after, placeholder) {
    const editor = el.markdownEditor;
    const start = editor.selectionStart;
    const end = editor.selectionEnd;
    const selected = editor.value.slice(start, end) || placeholder;
    const replacement = `${before}${selected}${after}`;
    editor.setRangeText(replacement, start, end, 'end');
    const selectionStart = start + before.length;
    editor.setSelectionRange(selectionStart, selectionStart + selected.length);
    editor.focus();
    editor.dispatchEvent(new Event('input', { bubbles: true }));
  }

  function prefixSelectedLines(prefix) {
    const editor = el.markdownEditor;
    const start = editor.selectionStart;
    const end = editor.selectionEnd;
    const value = editor.value;
    const lineStart = value.lastIndexOf('\n', start - 1) + 1;
    const nextNewline = value.indexOf('\n', end);
    const lineEnd = nextNewline === -1 ? value.length : nextNewline;
    const selected = value.slice(lineStart, lineEnd);
    const replacement = selected.split('\n').map(line => `${prefix}${line}`).join('\n');
    editor.setRangeText(replacement, lineStart, lineEnd, 'select');
    editor.focus();
    editor.dispatchEvent(new Event('input', { bubbles: true }));
  }

  async function handlePaste(event) {
    if (state.editorMode !== 'edit') return;

    const files = clipboardFiles(event.clipboardData);
    if (files.length > 0) {
      event.preventDefault();
      await insertFiles(files, 'paste');
      return;
    }

    // Some applications copy images as data URLs inside HTML rather than as a File.
    const html = event.clipboardData?.getData('text/html') || '';
    const dataImage = html.match(/<img[^>]+src=["'](data:image\/[^"']+)["']/i)?.[1];
    if (dataImage) {
      event.preventDefault();
      try {
        const blob = await fetch(dataImage).then(response => response.blob());
        const extension = blob.type.split('/')[1]?.replace('jpeg', 'jpg') || 'png';
        await insertFiles([new File([blob], `pasted-image-${timestampForName()}.${extension}`, { type: blob.type })], 'paste');
      } catch (error) {
        showToast(`Could not paste image: ${error.message}`, true);
      }
      return;
    }

    if (event.currentTarget === el.previewPane) {
      if (event.target.closest('.media-source-editor')) return;
      event.preventDefault();
      const block = event.target.closest('.live-block') || state.activeLiveBlock;
      if (block && block !== state.activeLiveBlock) activateLiveBlock(block);
      const text = event.clipboardData?.getData('text/plain') || '';
      insertTextIntoActiveBlock(text);
    }
  }

  function clipboardFiles(clipboardData) {
    if (!clipboardData) return [];
    const files = [];
    for (const item of clipboardData.items || []) {
      if (item.kind === 'file') {
        const file = item.getAsFile();
        if (file) files.push(file);
      }
    }
    return files.length ? files : [...(clipboardData.files || [])];
  }

  function handleDragEnter(event) {
    if (state.editorMode !== 'edit' || !hasFiles(event.dataTransfer)) return;
    event.preventDefault();
    state.dragDepth += 1;
    el.dropOverlay.hidden = false;
  }


  function handleDragOver(event) {
    if (state.editorMode !== 'edit' || !hasFiles(event.dataTransfer)) return;
    event.preventDefault();
    event.dataTransfer.dropEffect = 'copy';
  }


  function handleDragLeave(event) {
    if (state.editorMode !== 'edit' || !hasFiles(event.dataTransfer)) return;
    state.dragDepth = Math.max(0, state.dragDepth - 1);
    if (state.dragDepth === 0) el.dropOverlay.hidden = true;
  }


  async function handleDrop(event) {
    if (!hasFiles(event.dataTransfer)) return;
    event.preventDefault();
    state.dragDepth = 0;
    el.dropOverlay.hidden = true;
    if (state.editorMode !== 'edit') {
      showToast('Switch to Edit mode to insert files.');
      return;
    }
    await insertFiles([...event.dataTransfer.files], 'drop');
  }


  function hasFiles(dataTransfer) {
    return [...(dataTransfer?.types || [])].includes('Files');
  }

  function captureAttachmentInsertionPoint() {
    if (state.view === 'source') {
      return {
        view: 'source',
        start: el.markdownEditor.selectionStart,
        end: el.markdownEditor.selectionEnd,
      };
    }

    const blocks = [...el.previewPane.querySelectorAll(':scope > .live-block')];
    const anchor = state.activeLiveBlock || blocks.at(-1) || null;
    return { view: 'live', anchor };
  }

  function setAttachmentBusy(busy) {
    state.attachmentBusy = Boolean(busy);
    el.app.dataset.attachmentBusy = String(state.attachmentBusy);
    el.attachButton.disabled = state.attachmentBusy;
    el.attachInput.disabled = state.attachmentBusy;
    el.markdownEditor.readOnly = state.attachmentBusy || state.editorMode === 'view';
    el.previewPane.inert = state.attachmentBusy;
  }

  async function insertFiles(files, source = 'picker') {
    if (state.editorMode !== 'edit' || state.attachmentBusy) return;
    files = [...(files || [])].filter(file => file instanceof File);
    if (files.length === 0) return;

    const bookmark = captureAttachmentInsertionPoint();
    setAttachmentBusy(true);

    try {
      const inserted = state.attachmentHandler
        ? await uploadAttachmentsThroughHost(files, source)
        : await createStandaloneAttachments(files);

      if (!inserted.length) {
        showToast('Attachment upload was cancelled or returned no usable files.');
        return;
      }

      if (bookmark.view === 'live') {
        await insertAttachmentsIntoLiveEditor(inserted, bookmark.anchor);
      } else {
        insertAttachmentsIntoSourceEditor(inserted, bookmark);
      }

      showToast(`${inserted.length} attachment${inserted.length === 1 ? '' : 's'} inserted`);
    } catch (error) {
      console.error(error);
      showToast(`Attachment upload failed: ${error.message || error}`, true);
    } finally {
      setAttachmentBusy(false);
    }
  }

  async function createStandaloneAttachments(files) {
    const inserted = [];
    for (const file of files) {
      try {
        const record = await storeAttachment(file);
        const label = escapeMarkdownLabel(record.name || 'attachment');
        const kind = isImageFile(file) ? 'image' : isVideoFile(file) ? 'video' : 'file';
        const target = `attachment://${record.id}`;
        inserted.push({
          file,
          kind,
          label,
          url: target,
          snippet: attachmentMarkdown(kind, label, target),
        });
      } catch (error) {
        console.error(error);
        showToast(`Could not store ${file.name}: ${error.message}`, true);
      }
    }
    return inserted;
  }

  async function uploadAttachmentsThroughHost(files, source) {
    let returned;
    try {
      returned = await state.attachmentHandler([...files], { source });
    } catch (error) {
      throw new Error(error?.message || 'The host attachment handler rejected the files.');
    }

    if (returned == null) return [];
    if (!Array.isArray(returned)) {
      throw new TypeError('The host attachment handler must return an array of uploaded attachments.');
    }

    const inserted = [];
    let invalidCount = 0;
    returned.forEach((result, index) => {
      if (!result) return;
      const original = result.file instanceof File ? result.file : files[index] || null;
      const markdown = typeof result.markdown === 'string' ? result.markdown.trim() : '';
      if (markdown) {
        inserted.push({
          file: original,
          kind: normaliseAttachmentKind(result.kind, original),
          label: result.label || original?.name || 'attachment',
          url: typeof result.url === 'string' ? result.url : '',
          snippet: markdown,
        });
        return;
      }

      const url = typeof result.url === 'string' ? result.url.trim() : '';
      if (!url) {
        invalidCount += 1;
        return;
      }
      const kind = normaliseAttachmentKind(result.kind, original);
      const label = escapeMarkdownLabel(result.label || original?.name || 'attachment');
      inserted.push({ file: original, kind, label, url, snippet: attachmentMarkdown(kind, label, url) });
    });

    if (invalidCount) {
      showToast(`${invalidCount} uploaded attachment${invalidCount === 1 ? '' : 's'} had no URL or Markdown and were skipped.`, true);
    }
    return inserted;
  }

  function normaliseAttachmentKind(kind, file) {
    if (kind === 'image' || kind === 'video' || kind === 'file') return kind;
    if (file && isImageFile(file)) return 'image';
    if (file && isVideoFile(file)) return 'video';
    return 'file';
  }

  function attachmentMarkdown(kind, label, url) {
    if (kind === 'image') return `![${label}](${url})`;
    if (kind === 'video') return `!video[${label}](${url})`;
    return `[${label}](${url})`;
  }

  function insertAttachmentsIntoSourceEditor(inserted, bookmark) {
    const editor = el.markdownEditor;
    const start = Math.max(0, Math.min(editor.value.length, bookmark.start));
    const end = Math.max(start, Math.min(editor.value.length, bookmark.end));
    const snippets = inserted.map(item => item.snippet);
    const prefix = start > 0 && !/\s$/.test(editor.value.slice(0, start)) ? '\n\n' : '';
    const suffix = start < editor.value.length && !/^\s/.test(editor.value.slice(start)) ? '\n\n' : '';
    editor.setRangeText(prefix + snippets.join('\n\n') + suffix, start, end, 'end');
    editor.focus();
    editor.dispatchEvent(new Event('input', { bubbles: true }));
  }

  async function insertAttachmentsIntoLiveEditor(inserted, bookmarkedAnchor = null) {
    let anchor = bookmarkedAnchor?.isConnected
      ? bookmarkedAnchor
      : state.activeLiveBlock || el.previewPane.querySelector(':scope > .live-block:last-child');
    if (!anchor) {
      anchor = createLiveBlock('');
      el.previewPane.appendChild(anchor);
    }

    if (state.activeLiveBlock) commitActiveLiveBlock({ sync: false });

    const snippets = inserted.map(item => item.snippet);
    let reference = anchor;
    if ((anchor.dataset.source || '') === '') {
      anchor.dataset.source = snippets.shift();
      renderLiveBlock(anchor);
      await hydrateAttachments(anchor);
      await hydrateVideoPlayers(anchor);
      reference = anchor;
    }

    for (const snippet of snippets) {
      const block = createLiveBlock(snippet);
      reference.after(block);
      await hydrateAttachments(block);
      await hydrateVideoPlayers(block);
      reference = block;
    }

    const trailing = ensureTrailingLiveBlankBlock();
    hydrateWikiLinks(el.previewPane);
    syncLiveEditorToNote();
    activateLiveBlock(trailing, 0);
  }

  async function storeAttachment(file) {
    const id = uuid();
    const name = cleanFileName(file.name || `attachment-${timestampForName()}`);
    const record = {
      id,
      name,
      mimeType: file.type || 'application/octet-stream',
      size: file.size,
      blob: file.slice(0, file.size, file.type || 'application/octet-stream'),
      createdAt: Date.now(),
    };
    await putRecord(ATTACHMENT_STORE, record);
    return record;
  }

  function cleanFileName(name) {
    return name.replace(/[\\/:*?"<>|]/g, '_').slice(0, 220) || 'attachment';
  }

  function escapeMarkdownLabel(name) {
    return cleanFileName(name).replace(/([\[\]])/g, '\\$1');
  }

  function isImageFile(file) {
    return file.type.startsWith('image/') || /\.(png|jpe?g|gif|webp|svg|bmp|avif)$/i.test(file.name);
  }

  function isVideoFile(file) {
    return file.type.startsWith('video/') || /\.(mp4|m4v|webm|ogv|ogg|mov)$/i.test(file.name);
  }








  function timestampForName() {
    return new Date().toISOString().replace(/[:.]/g, '-');
  }

  function formatBytes(bytes) {
    if (!Number.isFinite(bytes) || bytes <= 0) return '0 B';
    const units = ['B', 'KB', 'MB', 'GB'];
    const index = Math.min(Math.floor(Math.log(bytes) / Math.log(1024)), units.length - 1);
    return `${(bytes / (1024 ** index)).toFixed(index === 0 ? 0 : 1)} ${units[index]}`;
  }


  function dispatchEditorEvent(name, detail) {
    if (!mountHost) return;
    mountHost.dispatchEvent(new CustomEvent(name, {
      detail,
      bubbles: true,
      composed: true,
    }));
  }

  function dispatchDocumentChange() {
    scheduleDocumentOutlineUpdate();
    dispatchEditorEvent('slate:change', { markdown: selectedNote()?.content || '' });
  }

  function exposePublicApi() {
    mountedApi = Object.freeze({
      version: APP_VERSION,
      host: mountHost,
      getMarkdown() {
        return selectedNote()?.content || '';
      },
      async setMarkdown(markdown) {
        const note = selectedNote();
        if (!note) return;
        note.content = ensureTrailingBlankLine(String(markdown ?? ''));
        note.updatedAt = Date.now();
        el.markdownEditor.value = note.content;
        scheduleSave(note);
        updateDocumentStats();
        await renderPreview();
        dispatchDocumentChange();
      },
      getWorkspaceMode() {
        return getWorkspaceMode();
      },
      setWorkspaceMode(mode) {
        return applyWorkspaceMode(mode);
      },
      setReadOnly(readOnly) {
        return applyReadOnly(readOnly);
      },
      setInteractiveTasks(enabled) {
        state.interactiveTasks = Boolean(enabled);
        applyPreviewInteractivity();
        return state.interactiveTasks;
      },
      setAttachmentHandler(handler) {
        validateAttachmentHandler(handler);
        state.attachmentHandler = handler;
        pendingAttachmentHandler = handler;
      },
      isOutlineOpen() {
        return state.outlineOpen;
      },
      setOutlineOpen(open) {
        applyOutlineVisibility(open);
      },
      get documentComplete() {
        return state.completionState.complete;
      },
      isDocumentComplete() {
        return state.completionState.complete;
      },
      getCompletionState() {
        return { ...state.completionState };
      },
      onDocumentComplete(callback, options = {}) {
        if (typeof callback !== 'function') throw new TypeError('onDocumentComplete requires a callback function.');
        state.completionCallbacks.add(callback);
        if (state.completionState.complete && options.immediate !== false) {
          queueMicrotask(() => {
            if (state.completionCallbacks.has(callback)) callback({ ...state.completionState });
          });
        }
        return () => state.completionCallbacks.delete(callback);
      },
      focus() {
        if (state.editorMode !== 'edit') return;
        if (state.view === 'source') el.markdownEditor.focus();
        else placeCaretAtEndIfEmpty(el.previewPane);
      }
    });

    const methodNames = [
      'getMarkdown', 'setMarkdown', 'getWorkspaceMode', 'setWorkspaceMode', 'setReadOnly',
      'isOutlineOpen', 'setOutlineOpen', 'isDocumentComplete', 'getCompletionState',
      'onDocumentComplete', 'focus'
    ];
    methodNames.forEach(name => {
      facade[name] = (...args) => mountedApi[name](...args);
    });
    facade.version = APP_VERSION;
    facade.host = mountHost;
  }

  function readSetting(key) {
    if (!state.persistSettings) return memorySettings.get(key) ?? null;
    try {
      return window.localStorage.getItem(key);
    } catch {
      return memorySettings.get(key) ?? null;
    }
  }

  function writeSetting(key, value) {
    memorySettings.set(key, String(value));
    if (!state.persistSettings) return;
    try {
      window.localStorage.setItem(key, String(value));
    } catch {
      // Some privacy modes block localStorage. The editor still works for this tab.
    }
  }

  function removeSetting(key) {
    memorySettings.delete(key);
    if (!state.persistSettings) return;
    try {
      window.localStorage.removeItem(key);
    } catch {
      // Ignore blocked localStorage.
    }
  }



  function showToast(message, isError = false) {
    const toast = document.createElement('div');
    toast.className = `toast${isError ? ' error' : ''}`;
    toast.textContent = message;
    el.toastRegion.appendChild(toast);
    setTimeout(() => toast.remove(), 3200);
  }

  function showFatalError(error) {
    if (!mountRoot) return;
    mountRoot.innerHTML = `
      <main class="slate-fatal-error">
        <h1>Slate Markdown Editor could not start</h1>
        <p>${escapeHtml(error?.message || String(error))}</p>
        <p>Try opening the page in a current version of Chrome, Edge, Firefox, or Safari. Private browsing modes may block persistent storage.</p>
      </main>`;
  }

})();
