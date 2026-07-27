import { useMemo, useState } from 'react';
import {
  Avatar,
  Badge,
  Button,
  EmptyState,
  IconButton,
  Modal,
  Panel,
  SearchField,
  Stack,
  Text,
  EyeIcon,
  EyeOffIcon,
  PencilIcon,
  PlusIcon,
  TrashIcon,
  UserIcon,
  useLiveQuery,
  useT,
  type ServiceContextProps,
} from '@holistic/ui';
import type { Contact, ContactsResponse } from './types';
import { ExternalEditor } from './ExternalEditor';
import { GroupsPanel } from './GroupsPanel';

// The contacts dashboard: every visible contact (internal — computed from shared privleg contact
// groups — plus external, owned by the user), a hidden section, and add/edit/hide/delete actions.
// Internal contacts can only be hidden; external contacts are fully editable and deletable.
export function Dashboard({ api, ui }: ServiceContextProps) {
  const t = useT();
  const q = useLiveQuery<ContactsResponse>(() => api.get<ContactsResponse>('contacts'), 15000);
  const [search, setSearch] = useState('');
  const [editor, setEditor] = useState<{ open: boolean; contact?: Contact }>({ open: false });
  const [hiddenOpen, setHiddenOpen] = useState(false);

  const contacts = q.data?.contacts ?? [];
  const filtered = useMemo(() => {
    const s = search.trim().toLowerCase();
    if (!s) return contacts;
    return contacts.filter((c) =>
      [c.displayName, c.firstName, c.lastName, c.nickname, c.email].some((f) => f.toLowerCase().includes(s)),
    );
  }, [contacts, search]);
  const visible = filtered.filter((c) => !c.hidden);
  const hidden = filtered.filter((c) => c.hidden);

  async function run(fn: () => Promise<unknown>, okMsg: string) {
    try {
      await fn();
      q.refresh();
      ui.toast({ title: okMsg, variant: 'success' });
    } catch (e) {
      ui.toast({ title: t('contax.actionFailed'), description: (e as Error).message, variant: 'error' });
    }
  }

  function setHidden(c: Contact, hide: boolean) {
    const verb = hide ? 'hide' : 'unhide';
    const path =
      c.kind === 'internal'
        ? `internal/${encodeURIComponent(c.username ?? c.id)}/${verb}`
        : `contacts/${encodeURIComponent(c.id)}/${verb}`;
    return run(() => api.post(path), hide ? t('contax.hidden') : t('contax.shown'));
  }

  async function remove(c: Contact) {
    const ok = await ui.confirm({
      title: t('contax.deleteTitle', { name: c.displayName }),
      description: t('contax.deleteContactBody'),
      danger: true,
      confirmLabel: t('common.delete'),
    });
    if (!ok) return;
    return run(() => api.del(`contacts/${encodeURIComponent(c.id)}`), t('contax.deleted'));
  }

  return (
    <Stack gap={4}>
      <Stack direction="row" align="center" justify="between" gap={3} wrap>
        <SearchField value={search} onChange={setSearch} placeholder={t('contax.searchContacts')} className="w-72 max-w-full" />
        <Stack direction="row" align="center" gap={2} wrap>
          <Button variant="secondary" iconLeft={<EyeOffIcon className="h-4 w-4" />} onClick={() => setHiddenOpen(true)}>
            {t('contax.hidden')}{hidden.length ? ` (${hidden.length})` : ''}
          </Button>
          <Button variant="primary" iconLeft={<PlusIcon className="h-4 w-4" />} onClick={() => setEditor({ open: true })}>
            {t('contax.addExternalContact')}
          </Button>
        </Stack>
      </Stack>

      <Panel title={t('contax.contacts')} className="overflow-hidden">
        {visible.length === 0 ? (
          <EmptyState
            icon={<UserIcon />}
            title={search ? t('common.noMatches') : t('contax.noContactsTitle')}
            description={q.loading ? t('contax.loading') : t('contax.noContactsBody')}
          />
        ) : (
          <Stack gap={0}>
            {visible.map((c) => (
              <ContactRow
                key={rowKey(c)}
                c={c}
                onHide={() => setHidden(c, true)}
                onEdit={() => setEditor({ open: true, contact: c })}
                onDelete={() => remove(c)}
              />
            ))}
          </Stack>
        )}
      </Panel>

      <GroupsPanel api={api} ui={ui} />

      <Modal
        open={hiddenOpen}
        onOpenChange={(o) => !o && setHiddenOpen(false)}
        title={t('contax.hiddenContacts')}
        size="lg"
        bodyClassName="p-0"
      >
        {hidden.length === 0 ? (
          <EmptyState
            icon={<EyeOffIcon />}
            title={t('contax.noHiddenTitle')}
            description={t('contax.noHiddenBody')}
          />
        ) : (
          <Stack gap={0}>
            {hidden.map((c) => (
              <ContactRow
                key={rowKey(c)}
                c={c}
                hiddenSection
                onShow={() => setHidden(c, false)}
                onEdit={() => {
                  setHiddenOpen(false);
                  setEditor({ open: true, contact: c });
                }}
                onDelete={() => remove(c)}
              />
            ))}
          </Stack>
        )}
      </Modal>

      <ExternalEditor
        open={editor.open}
        contact={editor.contact}
        api={api}
        ui={ui}
        onClose={() => setEditor({ open: false })}
        onSaved={() => {
          setEditor({ open: false });
          q.refresh();
        }}
      />
    </Stack>
  );
}

function rowKey(c: Contact) {
  return `${c.kind}:${c.id}`;
}

function ContactRow({
  c,
  hiddenSection,
  onHide,
  onShow,
  onEdit,
  onDelete,
}: {
  c: Contact;
  hiddenSection?: boolean;
  onHide?: () => void;
  onShow?: () => void;
  onEdit: () => void;
  onDelete: () => void;
}) {
  const t = useT();
  return (
    <Stack direction="row" align="center" gap={3} className="border-b border-separator px-4 py-2.5 last:border-b-0">
      <Avatar name={c.displayName} src={c.avatarUrl || undefined} size={36} />
      <Stack gap={0} className="min-w-0 flex-1">
        <Stack direction="row" align="center" gap={2}>
          <Text weight="medium" truncate>
            {c.displayName}
          </Text>
          <Badge variant={c.kind === 'internal' ? 'accent' : 'neutral'}>
            {c.kind === 'internal' ? t('contax.internal') : t('contax.external')}
          </Badge>
        </Stack>
        <Text variant="footnote" color="secondary" truncate>
          {c.email}
        </Text>
      </Stack>
      <Stack direction="row" align="center" gap={1}>
        {c.editable && (
          <IconButton label={t('contax.edit')} size="sm" onClick={onEdit}>
            <PencilIcon className="h-4 w-4" />
          </IconButton>
        )}
        {hiddenSection ? (
          <IconButton label={t('contax.show')} size="sm" onClick={onShow}>
            <EyeIcon className="h-4 w-4" />
          </IconButton>
        ) : (
          <IconButton label={t('contax.hide')} size="sm" onClick={onHide}>
            <EyeOffIcon className="h-4 w-4" />
          </IconButton>
        )}
        {c.editable && (
          <IconButton label={t('common.delete')} size="sm" onClick={onDelete}>
            <TrashIcon className="h-4 w-4" />
          </IconButton>
        )}
      </Stack>
    </Stack>
  );
}
