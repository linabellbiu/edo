<script setup lang="ts">
import { nextTick, onBeforeUnmount, ref, watch } from 'vue'
import { FitAddon } from '@xterm/addon-fit'
import { Terminal } from '@xterm/xterm'

const props=defineProps<{open:boolean;title:string;path:string}>()
const emit=defineEmits<{'update:open':[value:boolean]}>()
const container=ref<HTMLDivElement|null>(null)
let dispose:undefined|(()=>void)

async function connect(){dispose?.();await nextTick();if(!container.value||!props.path)return;const terminal=new Terminal({cursorBlink:true,fontFamily:'SFMono-Regular,Consolas,monospace',fontSize:13,theme:{background:'#050b12',foreground:'#dbe7f4',cursor:'#55d6be',selectionBackground:'#234b55'}}),fit=new FitAddon();terminal.loadAddon(fit);terminal.open(container.value);fit.fit();terminal.writeln('\x1b[36m正在建立 EDO WebSocket 终端…\x1b[0m');const protocol=location.protocol==='https:'?'wss:':'ws:',separator=props.path.includes('?')?'&':'?',socket=new WebSocket(`${protocol}//${location.host}${props.path}${separator}columns=${terminal.cols}&rows=${terminal.rows}`,'edo-terminal-v1'),encoder=new TextEncoder(),decoder=new TextDecoder();socket.binaryType='arraybuffer';socket.onmessage=(event)=>{if(event.data instanceof ArrayBuffer){terminal.write(decoder.decode(event.data,{stream:true}));return}try{const value=JSON.parse(String(event.data)) as {type?:string;message?:string};if(value.type==='ready')terminal.writeln('\x1b[32m终端已连接。\x1b[0m');if(value.type==='error'||value.type==='exit')terminal.writeln(`\r\n\x1b[33m${value.message||'终端会话已结束'}\x1b[0m`)}catch{terminal.writeln('\r\n\x1b[31m收到无法识别的终端事件。\x1b[0m')}};socket.onclose=()=>terminal.writeln('\r\n\x1b[90m连接已关闭。\x1b[0m');socket.onerror=()=>terminal.writeln('\r\n\x1b[31mWebSocket 终端连接失败。\x1b[0m');const input=terminal.onData(data=>{if(socket.readyState===WebSocket.OPEN)socket.send(encoder.encode(data))}),resize=terminal.onResize(({cols,rows})=>{if(socket.readyState===WebSocket.OPEN)socket.send(JSON.stringify({type:'resize',columns:cols,rows}))}),observer=new ResizeObserver(()=>{try{fit.fit()}catch{/* 关闭时终端 DOM 可能已释放。 */}});observer.observe(container.value);dispose=()=>{observer.disconnect();input.dispose();resize.dispose();if(socket.readyState===WebSocket.OPEN)socket.close(1000,'用户关闭终端');terminal.dispose()}}
watch(()=>props.open,(open)=>{if(open)void connect();else{dispose?.();dispose=undefined}})
onBeforeUnmount(()=>dispose?.())
</script>

<template><a-drawer :open="open" :title="title" placement="bottom" height="72vh" class="terminal-drawer" @close="emit('update:open',false)"><div ref="container" class="terminal-canvas"/></a-drawer></template>
<style scoped>.terminal-canvas{height:calc(72vh - 100px);padding:10px;border-radius:6px;background:#050b12}</style>
