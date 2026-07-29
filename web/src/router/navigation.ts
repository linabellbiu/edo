import type { Component } from 'vue'
import {
  Activity, AppWindow, Boxes, Box, Building2, ChartNoAxesCombined, CloudCog,
  Container, FileClock, FileText, GitBranch, KeyRound, ListChecks, PackageCheck,
  PanelsTopLeft, Play, Rocket, ScrollText, Server, ServerCog, Settings, ShieldCheck,
  Tags, UsersRound, Workflow,
} from 'lucide-vue-next'

export interface NavItem {
  label: string
  path: string
  permissions: string[]
  icon?: Component
}

export interface NavBranch {
  key: string
  label: string
  icon: Component
  items: NavItem[]
}

export interface NavSection {
  label?: string
  items?: NavItem[]
  branches?: NavBranch[]
}

export const navigation: NavSection[] = [
  { items: [{ label: 'nav.overview', path: '/', permissions: ['system.read'], icon: ChartNoAxesCombined }] },
  {
    label: 'nav.delivery',
    branches: [
      { key: 'applications-code', label: 'nav.applicationsCode', icon: AppWindow, items: [
        { label: 'nav.applications', path: '/applications', permissions: ['delivery.read'], icon: PanelsTopLeft },
        { label: 'nav.repositories', path: '/repositories', permissions: ['repository.read'], icon: GitBranch },
        { label: 'nav.credentials', path: '/credentials', permissions: ['credential.read'], icon: KeyRound },
      ] },
      { key: 'build-artifacts', label: 'nav.buildArtifacts', icon: PackageCheck, items: [
        { label: 'nav.buildPlans', path: '/build-plans', permissions: ['delivery.read'], icon: Boxes },
        { label: 'nav.registries', path: '/image-registries', permissions: ['delivery.read'], icon: Box },
      ] },
      { key: 'release-management', label: 'nav.releaseManagement', icon: Rocket, items: [
        { label: 'nav.deploymentPlans', path: '/deployment-plans', permissions: ['delivery.read'], icon: CloudCog },
        { label: 'nav.pipelinePlans', path: '/pipeline-plans', permissions: ['delivery.read'], icon: Workflow },
        { label: 'nav.releasePlans', path: '/release-plans?view=plans', permissions: ['delivery.read'], icon: ListChecks },
        { label: 'nav.pipelineRuns', path: '/release-plans?view=runs', permissions: ['delivery.read'], icon: Play },
        { label: 'nav.deploymentRecords', path: '/release-plans?view=records', permissions: ['deployment.read'], icon: FileClock },
      ] },
    ],
  },
  {
    label: 'nav.platform',
    branches: [
      { key: 'infrastructure', label: 'nav.infrastructure', icon: Container, items: [
        { label: 'nav.domains', path: '/domains', permissions: ['dns.read'], icon: Tags },
        { label: 'nav.environments', path: '/environments', permissions: ['deployment.read'], icon: ServerCog },
        { label: 'nav.hosts', path: '/hosts', permissions: ['cluster.read', 'deployment.read'], icon: Server },
      ] },
      { key: 'operations', label: 'nav.operations', icon: Activity, items: [
        { label: 'nav.monitor', path: '/system-monitor', permissions: ['monitor.read'], icon: Activity },
        { label: 'nav.tasks', path: '/operations?section=tasks', permissions: ['task.read'], icon: ListChecks },
        { label: 'nav.logs', path: '/logs', permissions: ['delivery.read'], icon: FileText },
      ] },
      { key: 'security', label: 'nav.security', icon: ShieldCheck, items: [
        { label: 'nav.settings', path: '/settings', permissions: [], icon: Settings },
        { label: 'nav.users', path: '/access?view=users', permissions: ['user.read', 'user.manage'], icon: UsersRound },
        { label: 'nav.roles', path: '/access?view=roles', permissions: ['role.read', 'role.manage', 'user.manage'], icon: ShieldCheck },
        { label: 'nav.audit', path: '/access?view=audit', permissions: ['audit.read'], icon: ScrollText },
      ] },
    ],
  },
]

export function flatNavigation() {
  return navigation.flatMap((section) => [
    ...(section.items ?? []),
    ...(section.branches ?? []).flatMap((branch) => branch.items),
  ])
}
