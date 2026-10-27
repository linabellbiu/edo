import assert from 'node:assert/strict'
import { test } from 'node:test'
import { loadFrontend } from './acceptanceHarness.mjs'

const { validK3dBindAddress, defaultK3dBindAddress } = loadFrontend('../src/utils/k3dProvision.ts')

test('k3d 绑定地址接受 NAS 私网与其他明确的单播 IPv4', () => {
  for (const value of ['192.168.1.10', '10.0.0.2', '172.16.0.9', '100.64.0.1', '169.253.255.255', '169.255.0.1', '1.2.3.4', '126.255.255.254', '128.0.0.1', '223.255.255.254']) {
    assert.equal(validK3dBindAddress(value), true, value)
  }
})

test('k3d --api-port 不支持 IPv6，压缩、完整与大写单播写法也明确拒绝', () => {
  for (const value of ['fd00::10', 'fd12:3456:789a::2', 'fc00::1', 'fe80::1234', '2001:db8::10', '2001:0DB8:0000:0000:0000:0000:0000:0001', 'ff0::1', '1:2:3:4:5:6:7:8']) {
    assert.equal(validK3dBindAddress(value), false, value)
  }
})

test('k3d 不接受 IPv4 未指定、回环、链路本地、组播、广播和保留地址', () => {
  for (const value of ['0.0.0.0', '0.1.2.3', '127.0.0.1', '127.0.1.2', '127.255.255.255', '169.254.0.0', '169.254.1.2', '169.254.255.255', '224.0.0.1', '239.255.255.255', '240.0.0.1', '255.255.255.255']) {
    assert.equal(validK3dBindAddress(value), false, value)
  }
})

test('k3d 不接受 IPv6 未指定、回环和组播的等价写法', () => {
  for (const value of ['::', '::0', '0:0:0:0:0:0:0:0', '::1', '0:0:0:0:0:0:0:1', '0000:0000:0000:0000:0000:0000:0000:0001', 'ff00::', 'ff02::1', 'FF0E::1', 'ffff::ffff']) {
    assert.equal(validK3dBindAddress(value), false, value)
  }
})

test('k3d 拒绝 WHATWG URL 可兼容但非严格四段十进制的 IPv4', () => {
  for (const value of ['3232235777', '192.168.1', '192.168', '127.1', '1', '010.0.0.1', '192.168.001.1', '0300.0250.01.01', '0xc0.168.1.1', '0x7f000001', '192.168.1.256', '1.2.3.4.5', '1.2.3.4.', '1..2.3', '+1.2.3.4', '-1.2.3.4']) {
    assert.equal(validK3dBindAddress(value), false, value)
  }
})

test('k3d 拒绝 DNS、URL、端口、CIDR、IPv6 区域标识和空白包装', () => {
  for (const value of ['nas.local', 'localhost', 'example.com', 'https://192.168.1.10', 'http://[fd00::1]', '192.168.1.10:6443', '[fd00::1]:6443', '[fd00::1]', '192.168.1.10/24', 'fd00::1/64', 'fe80::1%eth0', 'fe80::1%25eth0', '[fe80::1%eth0]', 'user@192.168.1.10', '192.168.1.10?port=6443', '192.168.1.10#fragment', ' 192.168.1.10', '192.168.1.10 ', '192.168.\n1.10', '\tfd00::1', 'fd00::1\n', '１９２.１６８.１.１０', '192。168。1。10']) {
    assert.equal(validK3dBindAddress(value), false, JSON.stringify(value))
  }
})

test('k3d 拒绝无效 IPv6，不把冒号文本视为有效地址', () => {
  for (const value of [':', ':::', 'fd00:::1', 'fd00::1::2', 'fd00::g', '10000::1', '1:2:3:4:5:6:7', '1:2:3:4:5:6:7:8:9', 'fd00:0:0:0:0:0:0:0:1']) {
    assert.equal(validK3dBindAddress(value), false, value)
  }
})

test('k3d 拒绝 IPv4 兼容、映射和转换形式，包括回环与私网写法', () => {
  for (const value of ['::ffff:127.0.0.1', '::FFFF:7f00:1', '0:0:0:0:0:ffff:7f00:1', '::ffff:0.0.0.0', '::ffff:224.0.0.1', '::ffff:192.168.1.10', '0:0:0:0:0:FFFF:C0A8:010A', '::127.0.0.1', '::7f00:1', '::192.168.1.10', '::ffff:0:127.0.0.1', '0:0:0:0:ffff:0:c0a8:10a']) {
    assert.equal(validK3dBindAddress(value), false, value)
  }
})

test('k3d 地址校验对非字符串和空值安全失败', () => {
  for (const value of [undefined, null, '', 0, 3232235777, false, {}, [], ['192.168.1.10'], new String('192.168.1.10')]) {
    assert.equal(validK3dBindAddress(value), false)
  }
})

test('k3d 默认绑定地址仅来自有效的 host.address，不推断 DNS 或映射端口', () => {
  assert.equal(defaultK3dBindAddress(), '')
  for (const address of ['192.168.1.10', '198.51.100.10']) {
    assert.equal(defaultK3dBindAddress({ address }), address)
  }
  for (const address of ['', 'nas.example.com', '192.168.1.10:2222', '127.0.0.1', '169.254.1.2', '::', '::1', 'fd00::10', '2001:0DB8::1', '::ffff:192.168.1.10', 'https://nas.example.com']) {
    assert.equal(defaultK3dBindAddress({ address }), '', address)
  }
  assert.equal(defaultK3dBindAddress({ address: 'nas.example.com', mapped_address: '192.168.1.10', api_server: 'https://192.168.1.10:6443' }), '')
})
