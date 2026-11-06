"use strict";
var __defProp = Object.defineProperty;
var __getOwnPropDesc = Object.getOwnPropertyDescriptor;
var __getOwnPropNames = Object.getOwnPropertyNames;
var __hasOwnProp = Object.prototype.hasOwnProperty;
var __copyProps = (to, from, except, desc) => {
  if (from && typeof from === "object" || typeof from === "function") {
    for (let key of __getOwnPropNames(from))
      if (!__hasOwnProp.call(to, key) && key !== except)
        __defProp(to, key, { get: () => from[key], enumerable: !(desc = __getOwnPropDesc(from, key)) || desc.enumerable });
  }
  return to;
};
var __toCommonJS = (mod) => __copyProps(__defProp({}, "__esModule", { value: true }), mod);

// src/preload/index.ts
var index_exports = {};
module.exports = __toCommonJS(index_exports);
var import_electron = require("electron");
var api = {
  snapshot: () => import_electron.ipcRenderer.invoke("bp:snapshot"),
  signIn: (email, password) => import_electron.ipcRenderer.invoke("bp:sign-in", { email, password }),
  signOut: () => import_electron.ipcRenderer.invoke("bp:sign-out"),
  connect: (outbound) => import_electron.ipcRenderer.invoke("bp:connect", { outbound }),
  disconnect: () => import_electron.ipcRenderer.invoke("bp:disconnect"),
  setPrefs: (patch) => import_electron.ipcRenderer.invoke("bp:set-prefs", patch),
  routeHost: (host) => import_electron.ipcRenderer.invoke("bp:route-host", { host }),
  tab: (action, id, url) => import_electron.ipcRenderer.invoke("bp:tab", { action, id, url }),
  openExternal: (url) => import_electron.ipcRenderer.invoke("bp:open-external", { url }),
  overlay: (open) => import_electron.ipcRenderer.invoke("bp:overlay", { open }),
  diagnostics: () => import_electron.ipcRenderer.invoke("bp:diagnostics"),
  onSnapshot: (cb) => {
    import_electron.ipcRenderer.on("bp:snapshot", (_e, s) => cb(s));
  },
  onNotice: (cb) => {
    import_electron.ipcRenderer.on("bp:notice", (_e, n) => cb(n));
  }
};
import_electron.contextBridge.exposeInMainWorld("bp", api);
