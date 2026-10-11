import { unwrap } from "@talanta/shared";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useMemo, useState, type FormEvent } from "react";
import { SaveTemplateForm } from "../components/SaveTemplateForm";
import { StadiumPlan, zoomBox } from "../components/StadiumPlan";
import { StadiumIcon } from "../components/icons";
import { Alert, Button, Card, ConfirmDialog, LoadFailed, SelectField, SkeletonRows, StateBlock, Swatch, TextField } from "../components/ui";
import { api, type Schemas } from "../lib/api";
import { BLOCK_BUSY_MESSAGE, describeError, isCode, splitFieldErrors } from "../lib/errors";
import { formatDate } from "../lib/format";
import { placementFromBox, polygonPath, type Box } from "../lib/geometry";
import { formatCount, formatKes } from "../lib/money";
import {
  createBlock,
  generateSeats,
  keys,
  updateBlock,
  useAdminEvents,
  usePublishInFlight,
  useTemplates,
  useVenue,
  useVenues,
  type Block,
  type BlockLayout,
  type Template,
  type VenueDetail,
} from "../lib/queries";

const PUBLISHING_NOTE = "An event is being published right now. Blocks cannot be changed until that finishes (up to a minute).";

/** A plan number typed into a box. These are positions, not money, so decimals are fine. */
function readNumber(text: string): number | null {
  const trimmed = text.trim();
  if (!/^-?\d+(\.\d+)?$/.test(trimmed)) return null;
  const value = Number(trimmed);
  return Number.isFinite(value) ? value : null;
}

function readWhole(text: string): number | null {
  return /^\d{1,3}$/.test(text.trim()) ? Number.parseInt(text.trim(), 10) : null;
}

const show = (value: number) => String(Math.round(value * 100) / 100);

export function VenuePage() {
  const venues = useVenues();
  const [venueChoice, setVenueChoice] = useState<number | null>(null);
  const venueId = venueChoice ?? venues.data?.items[0]?.id;
  const venue = useVenue(venueId);

  return (
    <div className="page page--wide">
      <header className="page__header">
        <h1>Venue layout</h1>
        <p>
          The stadium's blocks and seats, shared by every event. Place a block, let the server generate its seats, and save
          an event's painting as a template. Painting categories is done per event, under Events.
        </p>
      </header>

      {venues.isPending || (venue.isPending && venueId !== undefined) ? (
        <Card>
          <div className="card__body card__body--flush">
            <SkeletonRows label="Loading the venue" rows={5} />
          </div>
        </Card>
      ) : venues.isError ? (
        <Card>
          <LoadFailed message={describeError(venues.error)} onRetry={() => venues.refetch()} retrying={venues.isFetching} />
        </Card>
      ) : venueId === undefined ? (
        <Card>
          <StateBlock icon={<StadiumIcon />} title="No venue has been set up">
            <p>Ask the site admin to load the stadium's layout.</p>
          </StateBlock>
        </Card>
      ) : venue.isError || !venue.data ? (
        <Card>
          <LoadFailed message={describeError(venue.error)} onRetry={() => venue.refetch()} retrying={venue.isFetching} />
        </Card>
      ) : (
        <>
          {venues.data.items.length > 1 && (
            <SelectField label="Venue" value={venueId} onChange={(e) => setVenueChoice(Number(e.target.value))}>
              {venues.data.items.map((item) => (
                <option key={item.id} value={item.id}>
                  {item.name}
                </option>
              ))}
            </SelectField>
          )}
          <LayoutEditor key={venue.data.id} venue={venue.data} />
          <TemplatesCard venue={venue.data} />
        </>
      )}
    </div>
  );
}

interface Draft {
  standId: number;
  code: string;
  label: string;
  originX: string;
  originY: string;
  rotation: string;
  arc: string;
  pitchX: string;
  pitchY: string;
  /** What fits the dragged rectangle; carried into the seat form once the block exists. */
  rows: number;
  seatsPerRow: number;
}

function LayoutEditor({ venue }: { venue: VenueDetail }) {
  const queryClient = useQueryClient();
  const publishing = usePublishInFlight();
  const [selectedId, setSelectedId] = useState<number | null>(null);
  const [draft, setDraft] = useState<Draft | null>(null);
  const [preview, setPreview] = useState<BlockLayout | null>(null);
  const [zoomed, setZoomed] = useState(false);
  const [touchDrag, setTouchDrag] = useState(false);
  /** Seats that fit the rectangle a new block was dragged with, keyed by block. */
  const [suggested, setSuggested] = useState<{ blockId: number; rows: number; seatsPerRow: number } | null>(null);
  const [notice, setNotice] = useState<string | null>(null);

  const blocks = useMemo(() => venue.stands.flatMap((stand) => stand.blocks), [venue]);
  const selected = selectedId === null ? null : (blocks.find((block) => block.id === selectedId) ?? null);
  const standOf = (block: Block) => venue.stands.find((stand) => stand.id === block.stand_id);
  const refreshVenue = () =>
    Promise.all([queryClient.invalidateQueries({ queryKey: keys.venue(venue.id) }), queryClient.invalidateQueries({ queryKey: keys.venues })]);

  const place = useMutation({
    mutationFn: (body: Schemas["CreateBlock"]) => createBlock(venue.id, body),
    onSuccess: async (block) => {
      if (draft) setSuggested({ blockId: block.id, rows: draft.rows, seatsPerRow: draft.seatsPerRow });
      await refreshVenue();
      setDraft(null);
      setPreview(null);
      setSelectedId(block.id);
      setZoomed(true);
      setNotice(`Block ${block.code} is placed. It has no seats yet: generate them below.`);
    },
  });

  function select(block: Block) {
    if (draft) return;
    setSelectedId((current) => (current === block.id ? null : block.id));
    setPreview(null);
    setNotice(null);
    setZoomed(false);
  }

  function startPlacing() {
    setSelectedId(null);
    setPreview(null);
    setNotice(null);
    setZoomed(false);
    place.reset();
    setDraft({
      standId: venue.stands[0]?.id ?? 0,
      code: "",
      label: "",
      originX: "",
      originY: "",
      rotation: "0",
      arc: "",
      pitchX: "5",
      pitchY: "8",
      rows: 10,
      seatsPerRow: 20,
    });
  }

  function dragged(box: Box) {
    if (!draft) return;
    const pitchX = readNumber(draft.pitchX);
    const pitchY = readNumber(draft.pitchY);
    if (!pitchX || !pitchY || pitchX <= 0 || pitchY <= 0) return;
    const fit = placementFromBox(box, pitchX, pitchY);
    setDraft({
      ...draft,
      originX: show(fit.originX),
      originY: show(fit.originY),
      rows: Math.min(200, fit.rows),
      seatsPerRow: Math.min(500, fit.seatsPerRow),
    });
    if (place.isError) place.reset();
  }

  const draftShape = draft ? draftRectangle(draft) : null;
  const focusPoints = preview && preview.outline.length > 0 ? preview.outline : selected && selected.outline.length > 0 ? selected.outline : selected ? [[selected.origin_x, selected.origin_y] as const] : null;
  const view = zoomed && focusPoints ? zoomBox(focusPoints, 60, { width: venue.plan_width, height: venue.plan_height }) : null;

  return (
    <div className="editor">
      <Card className="editor__plan">
        <div className="card__header">
          <h2>{venue.name}</h2>
          <span className="small muted">
            {formatCount(blocks.length)} blocks · {formatCount(venue.seat_count)} seats · plan {formatCount(venue.plan_width)} × {formatCount(venue.plan_height)}
          </span>
        </div>
        <div className="card__body stack">
          <div className="row">
            {draft ? (
              <span className="small">
                <strong>Placing a block.</strong> Drag a rectangle on the plan where it should go, or type its position.
              </span>
            ) : (
              <Button variant="secondary" size="small" disabled={publishing} onClick={startPlacing}>
                Place a new block
              </Button>
            )}
            {focusPoints && (
              <Button variant="ghost" size="small" onClick={() => setZoomed((on) => !on)}>
                {zoomed ? "Show the whole stadium" : "Zoom to this block"}
              </Button>
            )}
            {draft && (
              <label className="toggle">
                <input type="checkbox" checked={touchDrag} onChange={(e) => setTouchDrag(e.target.checked)} />
                Drag with my finger
              </label>
            )}
          </div>
          <div className="planwrap">
            <StadiumPlan
              venue={venue}
              label={`Plan of ${venue.name} with ${blocks.length} blocks`}
              selected={selectedId === null ? undefined : new Set([selectedId])}
              onBlockPress={draft ? undefined : select}
              onBoxDrag={draft ? dragged : undefined}
              touchDrag={touchDrag}
              view={view}
              look={(block, stand) => ({
                fill: block.has_inventory ? "var(--ink-200)" : "var(--champagne-300)",
                title: `Block ${block.code}, ${stand.name} ${stand.tier.toLowerCase()}, ${
                  block.seat_count === 0 ? "no seats yet" : `${formatCount(block.seat_count)} seats`
                }${block.has_inventory ? ", used by a published event" : ""}`,
              })}
            >
              {draftShape && (
                <g transform={`rotate(${draftShape.rotation} ${draftShape.originX} ${draftShape.originY})`}>
                  <rect className="plan__draft" x={draftShape.x} y={draftShape.y} width={draftShape.width} height={draftShape.height} />
                  <circle className="plan__origin" cx={draftShape.originX} cy={draftShape.originY} r="6" />
                </g>
              )}
              {preview && (
                <g className="plan__preview">
                  <path d={polygonPath(preview.outline)} className="plan__preview-outline" />
                  {preview.rows.map((row) =>
                    row.seats.map((seat) => <circle key={`${row.row_index}-${seat[1]}`} cx={seat[2]} cy={seat[3]} r="1.7" />),
                  )}
                </g>
              )}
            </StadiumPlan>
          </div>
          <ul className="legend" aria-label="Key">
            <li>
              <span className="swatch" style={{ background: "var(--champagne-300)" }} aria-hidden="true" /> Block
            </li>
            <li>
              <span className="swatch" style={{ background: "var(--ink-200)" }} aria-hidden="true" /> Used by a published event (seats are fixed)
            </li>
            <li>
              <span className="legend__dot legend__dot--origin" aria-hidden="true" /> Placed, no seats yet
            </li>
          </ul>
        </div>
      </Card>

      <div className="editor__panel stack">
        {publishing && <Alert tone="warning">{PUBLISHING_NOTE}</Alert>}
        {notice && <Alert tone="success">{notice}</Alert>}
        {draft ? (
          <PlaceForm
            venue={venue}
            draft={draft}
            onChange={(patch) => {
              setDraft({ ...draft, ...patch });
              if (place.isError) place.reset();
            }}
            error={place.error}
            busy={place.isPending}
            onSubmit={(body) => place.mutate(body)}
            onCancel={() => setDraft(null)}
          />
        ) : selected ? (
          <BlockPanel
            key={selected.id}
            block={selected}
            standName={standOf(selected) ? `${standOf(selected)!.name}, ${standOf(selected)!.tier.toLowerCase()} tier` : ""}
            suggested={suggested?.blockId === selected.id ? suggested : null}
            locked={publishing}
            preview={preview}
            onPreview={(layout) => {
              setPreview(layout);
              if (layout) setZoomed(true);
            }}
            onChanged={async (message) => {
              setPreview(null);
              await refreshVenue();
              setNotice(message);
            }}
            onDeleted={async (code) => {
              setSelectedId(null);
              setPreview(null);
              setZoomed(false);
              await refreshVenue();
              setNotice(`Block ${code} and its seats are deleted.`);
            }}
          />
        ) : (
          <Card>
            <StateBlock icon={<StadiumIcon />} title="Pick a block">
              <p>Click a block on the plan to move it, generate its seats or delete it. Or place a new one.</p>
            </StateBlock>
          </Card>
        )}
      </div>
    </div>
  );
}

/** The rectangle a draft block would cover before rotation or curve, for drawing on the plan. */
function draftRectangle(draft: Draft) {
  const originX = readNumber(draft.originX);
  const originY = readNumber(draft.originY);
  const pitchX = readNumber(draft.pitchX);
  const pitchY = readNumber(draft.pitchY);
  if (originX === null || originY === null || !pitchX || !pitchY) return null;
  return {
    originX,
    originY,
    x: originX - pitchX / 2,
    y: originY - pitchY / 2,
    width: draft.seatsPerRow * pitchX,
    height: draft.rows * pitchY,
    rotation: readNumber(draft.rotation) ?? 0,
  };
}

const PLACE_FIELDS = ["stand_id", "code", "label", "origin_x", "origin_y", "rotation_deg", "arc_radius", "seat_pitch_x", "seat_pitch_y"] as const;
type PlaceField = (typeof PLACE_FIELDS)[number];

/** Reads the placement boxes. Shared by "place" and "edit", which take the same fields. */
function readPlacement(input: { code: string; label: string; originX: string; originY: string; rotation: string; arc: string; pitchX: string; pitchY: string }) {
  const errors: Partial<Record<PlaceField, string>> = {};
  const code = input.code.trim().toUpperCase();
  if (!/^[A-Z0-9-]{1,12}$/.test(code)) errors.code = "Use up to 12 capital letters, digits and dashes, for example N12.";
  const label = input.label.trim();
  if (!label) errors.label = "Give the block a name people will recognise, for example North 12.";
  const need = (text: string, field: PlaceField, what: string) => {
    const value = readNumber(text);
    if (value === null) errors[field] = `Enter ${what} as a number.`;
    return value ?? 0;
  };
  const originX = need(input.originX, "origin_x", "the position");
  const originY = need(input.originY, "origin_y", "the position");
  const rotation = need(input.rotation, "rotation_deg", "the rotation");
  if (!errors.rotation_deg && (rotation < -360 || rotation > 360)) errors.rotation_deg = "Rotation is from -360 to 360 degrees.";
  const pitchX = need(input.pitchX, "seat_pitch_x", "the seat spacing");
  if (!errors.seat_pitch_x && pitchX <= 0) errors.seat_pitch_x = "Seat spacing must be more than 0.";
  const pitchY = need(input.pitchY, "seat_pitch_y", "the row spacing");
  if (!errors.seat_pitch_y && pitchY <= 0) errors.seat_pitch_y = "Row spacing must be more than 0.";
  let arc: number | null = null;
  if (input.arc.trim()) {
    arc = readNumber(input.arc);
    if (arc === null || arc <= 0) errors.arc_radius = "Enter the curve radius as a number above 0, or leave it empty for a straight block.";
  }
  return {
    errors,
    values: { code, label, origin_x: originX, origin_y: originY, rotation_deg: rotation, arc_radius: arc, seat_pitch_x: pitchX, seat_pitch_y: pitchY },
  };
}

function boundsHint(error: unknown): string | null {
  if (!isCode(error, "VALIDATION_FAILED")) return null;
  const details = error.details as { fields?: { rule: string }[] } | null;
  return details?.fields?.some((field) => field.rule === "plan_bounds")
    ? "With these values the block would stick out of the venue plan. Move it inwards, or make it smaller."
    : null;
}

function PlaceForm({
  venue,
  draft,
  onChange,
  error,
  busy,
  onSubmit,
  onCancel,
}: {
  venue: VenueDetail;
  draft: Draft;
  onChange: (patch: Partial<Draft>) => void;
  error: unknown;
  busy: boolean;
  onSubmit: (body: Schemas["CreateBlock"]) => void;
  onCancel: () => void;
}) {
  const [local, setLocal] = useState<Partial<Record<PlaceField, string>>>({});
  const { fields, other } = splitFieldErrors(error, PLACE_FIELDS);
  const taken = isCode(error, "ALREADY_EXISTS") ? "This stand already has a block with this code." : null;
  const errorFor = (field: PlaceField) => local[field] ?? fields[field] ?? null;
  const formError =
    error && !taken && Object.keys(fields).length === 0
      ? describeError(error, { NOT_FOUND: "That stand is not part of this venue any more. Reload the page." })
      : null;

  function submit(event: FormEvent) {
    event.preventDefault();
    if (busy) return;
    // An empty rotation box on a new block means "not turned".
    const { errors, values } = readPlacement({ ...draft, rotation: draft.rotation.trim() || "0" });
    if (!draft.originX.trim() && !draft.originY.trim()) {
      errors.origin_x = "Drag a rectangle on the plan, or type the position.";
      delete errors.origin_y;
    }
    setLocal(errors);
    if (Object.keys(errors).length > 0) return;
    // rotation_deg is only sent when the block is turned; left out, the server places it straight.
    const { rotation_deg, ...placement } = values;
    onSubmit({ stand_id: draft.standId, ...placement, ...(rotation_deg !== 0 ? { rotation_deg } : {}) });
  }

  const change = (patch: Partial<Draft>) => {
    setLocal({});
    onChange(patch);
  };

  return (
    <Card>
      <div className="card__header">
        <h2>Step 1: place a block</h2>
      </div>
      <form className="card__body stack" onSubmit={submit} noValidate>
        <div className="formgrid formgrid--tight">
          <div className="formgrid__wide">
            <SelectField label="Stand" name="stand_id" value={draft.standId} onChange={(e) => change({ standId: Number(e.target.value) })} error={errorFor("stand_id")}>
              {venue.stands.map((stand) => (
                <option key={stand.id} value={stand.id}>
                  {stand.name}, {stand.tier.toLowerCase()} tier
                </option>
              ))}
            </SelectField>
          </div>
          <TextField label="Code" name="code" maxLength={12} placeholder="N12" autoCapitalize="characters" value={draft.code} onChange={(e) => change({ code: e.target.value.toUpperCase() })} error={errorFor("code") ?? taken} />
          <TextField label="Name" name="label" maxLength={60} placeholder="North 12" value={draft.label} onChange={(e) => change({ label: e.target.value })} error={errorFor("label")} />
          <TextField label="First seat: across (x)" name="origin_x" inputMode="decimal" value={draft.originX} onChange={(e) => change({ originX: e.target.value })} error={errorFor("origin_x")} hint={`0 to ${venue.plan_width}, from the left`} />
          <TextField label="First seat: down (y)" name="origin_y" inputMode="decimal" value={draft.originY} onChange={(e) => change({ originY: e.target.value })} error={errorFor("origin_y")} hint={`0 to ${venue.plan_height}, from the top`} />
          <TextField label="Rotation, degrees clockwise" name="rotation_deg" inputMode="decimal" value={draft.rotation} onChange={(e) => change({ rotation: e.target.value })} error={errorFor("rotation_deg")} />
          <TextField label="Curve radius (optional)" name="arc_radius" inputMode="decimal" placeholder="Straight" value={draft.arc} onChange={(e) => change({ arc: e.target.value })} error={errorFor("arc_radius")} hint="Radius of the front row. Empty for a straight block." />
          <TextField label="Seat spacing" name="seat_pitch_x" inputMode="decimal" value={draft.pitchX} onChange={(e) => change({ pitchX: e.target.value })} error={errorFor("seat_pitch_x")} />
          <TextField label="Row spacing" name="seat_pitch_y" inputMode="decimal" value={draft.pitchY} onChange={(e) => change({ pitchY: e.target.value })} error={errorFor("seat_pitch_y")} />
        </div>
        {draft.originX.trim() !== "" && (
          <p className="small muted">
            The rectangle fits about {draft.rows} rows of {draft.seatsPerRow} seats. You choose the exact numbers in step 2.
          </p>
        )}
        {(formError || other.length > 0 || boundsHint(error)) && (
          <Alert tone="error">
            {boundsHint(error) ?? formError}
            {other.map((message) => (
              <p key={message}>{message}</p>
            ))}
          </Alert>
        )}
        <div className="form-actions">
          <Button type="submit" loading={busy}>
            Place block
          </Button>
          <Button variant="ghost" onClick={onCancel} disabled={busy}>
            Cancel
          </Button>
        </div>
      </form>
    </Card>
  );
}

function BlockPanel({
  block,
  standName,
  suggested,
  locked,
  preview,
  onPreview,
  onChanged,
  onDeleted,
}: {
  block: Block;
  standName: string;
  suggested: { rows: number; seatsPerRow: number } | null;
  locked: boolean;
  preview: BlockLayout | null;
  onPreview: (layout: BlockLayout | null) => void;
  onChanged: (message: string) => Promise<void>;
  onDeleted: (code: string) => Promise<void>;
}) {
  const [deleting, setDeleting] = useState(false);

  const remove = useMutation({
    mutationFn: () => unwrap(api.DELETE("/admin/blocks/{blockId}", { params: { path: { blockId: block.id } } })),
    onSuccess: () => {
      setDeleting(false);
      return onDeleted(block.code);
    },
  });

  return (
    <>
      <Card>
        <div className="card__header">
          <h2>Block {block.code}</h2>
          <span className="small muted">{standName}</span>
        </div>
        <div className="card__body stack">
          <p className="small">
            {block.label} ·{" "}
            {block.seat_count === 0
              ? "no seats yet"
              : `${formatCount(block.seat_count)} seats in ${block.row_count} rows of up to ${block.seats_per_row}`}
          </p>
          {block.has_inventory && (
            <Alert tone="info">
              A published event is selling this block's seats. It can still be moved or renamed, but its seats cannot be
              regenerated and it cannot be deleted.
            </Alert>
          )}
          <PlacementForm block={block} locked={locked} onSaved={onChanged} />
        </div>
      </Card>

      <SeatsForm block={block} suggested={suggested} locked={locked} preview={preview} onPreview={onPreview} onSaved={onChanged} />

      <Card>
        <div className="card__body form-actions">
          <Button variant="danger" size="small" disabled={locked} onClick={() => { remove.reset(); setDeleting(true); }}>
            Delete block {block.code}
          </Button>
          <span className="small muted">Removes the block and its seats from the venue.</span>
        </div>
      </Card>

      <ConfirmDialog
        open={deleting}
        title={`Delete block ${block.code}?`}
        confirmLabel={remove.isPending ? "Deleting…" : "Delete block"}
        cancelLabel="Keep it"
        danger
        busy={remove.isPending}
        error={remove.error ? inventoryMessage(remove.error, "deleted") : null}
        onConfirm={() => remove.mutate()}
        onCancel={() => setDeleting(false)}
      >
        <p>
          The block and its {formatCount(block.seat_count)} seats are removed from the venue, and from the painting of every
          draft event. <strong>This cannot be undone.</strong>
        </p>
      </ConfirmDialog>
    </>
  );
}

function inventoryMessage(error: unknown, action: "deleted" | "regenerated"): string {
  if (isCode(error, "BLOCK_HAS_INVENTORY")) {
    const ids = (error.details as { event_ids?: number[] } | null)?.event_ids ?? [];
    if (ids.length === 0) return `An event at this venue is being published right now, so the block cannot be ${action}. Try again in a minute.`;
    return `This block cannot be ${action}: ${ids.length === 1 ? `a published event (number ${ids[0]}) is` : `${ids.length} published events (numbers ${ids.join(", ")}) are`} selling its seats.`;
  }
  return describeError(error, { NOT_FOUND: "This block no longer exists. Reload the page." });
}

function PlacementForm({ block, locked, onSaved }: { block: Block; locked: boolean; onSaved: (message: string) => Promise<void> }) {
  const initial = () => ({
    code: block.code,
    label: block.label,
    originX: show(block.origin_x),
    originY: show(block.origin_y),
    rotation: show(block.rotation_deg),
    arc: block.arc_radius === null ? "" : show(block.arc_radius),
    pitchX: show(block.seat_pitch_x),
    pitchY: show(block.seat_pitch_y),
  });
  const [input, setInput] = useState(initial);
  const [local, setLocal] = useState<Partial<Record<PlaceField, string>>>({});
  /** The values the last save replaced: one step of undo. */
  const [undo, setUndo] = useState<Schemas["UpdateBlock"] | null>(null);

  const save = useMutation({
    mutationFn: (request: { body: Schemas["UpdateBlock"]; undoing: boolean }) => updateBlock(block.id, request.body),
    onSuccess: (saved, request) => {
      if (request.undoing) {
        setUndo(null);
      } else {
        // Remember what each sent field was before, exactly as the server had it.
        const before: Record<string, unknown> = {};
        for (const key of Object.keys(request.body)) before[key] = block[key as keyof Block];
        setUndo(before as Schemas["UpdateBlock"]);
      }
      setInput({
        code: saved.code,
        label: saved.label,
        originX: show(saved.origin_x),
        originY: show(saved.origin_y),
        rotation: show(saved.rotation_deg),
        arc: saved.arc_radius === null ? "" : show(saved.arc_radius),
        pitchX: show(saved.seat_pitch_x),
        pitchY: show(saved.seat_pitch_y),
      });
      return onSaved(request.undoing ? `Block ${saved.code} is back where it was.` : `Block ${saved.code} saved.`);
    },
  });

  function submit(event: FormEvent) {
    event.preventDefault();
    if (save.isPending || locked) return;
    const { errors, values } = readPlacement(input);
    setLocal(errors);
    if (Object.keys(errors).length > 0) return;
    // Compare with what the boxes showed at the start, so rounding for display is never sent as a change.
    const before = readPlacement(initial()).values;
    const body: Record<string, unknown> = {};
    for (const key of Object.keys(values) as (keyof typeof values)[]) {
      if (values[key] !== before[key]) body[key] = values[key];
    }
    if (Object.keys(body).length === 0) return;
    save.mutate({ body: body as Schemas["UpdateBlock"], undoing: false });
  }

  const change = (patch: Partial<typeof input>) => {
    setInput((current) => ({ ...current, ...patch }));
    setLocal({});
    if (save.isError) save.reset();
  };
  const { fields, other } = splitFieldErrors(save.error, PLACE_FIELDS);
  const taken = isCode(save.error, "ALREADY_EXISTS") ? "Another block in this stand has this code." : null;
  const errorFor = (field: PlaceField) => local[field] ?? fields[field] ?? null;
  const formError =
    save.error && !taken && Object.keys(fields).length === 0
      ? describeError(save.error, { NOT_FOUND: "This block no longer exists. Reload the page.", EVENT_BUSY: BLOCK_BUSY_MESSAGE })
      : null;

  return (
    <form className="stack" onSubmit={submit} noValidate>
      <fieldset className="formgrid formgrid--tight" disabled={locked}>
        <legend className="formgrid__legend">Position and shape</legend>
        <TextField label="Code" name="code" maxLength={12} value={input.code} onChange={(e) => change({ code: e.target.value.toUpperCase() })} error={errorFor("code") ?? taken} />
        <TextField label="Name" name="label" maxLength={60} value={input.label} onChange={(e) => change({ label: e.target.value })} error={errorFor("label")} />
        <TextField label="First seat: across (x)" name="origin_x" inputMode="decimal" value={input.originX} onChange={(e) => change({ originX: e.target.value })} error={errorFor("origin_x")} />
        <TextField label="First seat: down (y)" name="origin_y" inputMode="decimal" value={input.originY} onChange={(e) => change({ originY: e.target.value })} error={errorFor("origin_y")} />
        <TextField label="Rotation, degrees clockwise" name="rotation_deg" inputMode="decimal" value={input.rotation} onChange={(e) => change({ rotation: e.target.value })} error={errorFor("rotation_deg")} />
        <TextField label="Curve radius (optional)" name="arc_radius" inputMode="decimal" placeholder="Straight" value={input.arc} onChange={(e) => change({ arc: e.target.value })} error={errorFor("arc_radius")} />
        <TextField label="Seat spacing" name="seat_pitch_x" inputMode="decimal" value={input.pitchX} onChange={(e) => change({ pitchX: e.target.value })} error={errorFor("seat_pitch_x")} />
        <TextField label="Row spacing" name="seat_pitch_y" inputMode="decimal" value={input.pitchY} onChange={(e) => change({ pitchY: e.target.value })} error={errorFor("seat_pitch_y")} />
      </fieldset>
      {(formError || other.length > 0 || boundsHint(save.error)) && (
        <Alert tone="error">
          {boundsHint(save.error) ?? formError}
          {other.map((message) => (
            <p key={message}>{message}</p>
          ))}
        </Alert>
      )}
      <div className="form-actions">
        <Button type="submit" variant="secondary" size="small" loading={save.isPending && !save.variables?.undoing} disabled={locked}>
          Save position
        </Button>
        {undo && (
          <Button variant="ghost" size="small" loading={save.isPending && save.variables?.undoing} disabled={locked} onClick={() => save.mutate({ body: undo, undoing: true })}>
            Undo last change
          </Button>
        )}
        <span className="small muted">Moving a block keeps its seats and their numbers.</span>
      </div>
    </form>
  );
}

const SEAT_FIELDS = ["rows", "seats_per_row", "row_label_scheme", "numbering_direction", "row_offsets"] as const;
type SeatField = (typeof SEAT_FIELDS)[number];

function SeatsForm({
  block,
  suggested,
  locked,
  preview,
  onPreview,
  onSaved,
}: {
  block: Block;
  suggested: { rows: number; seatsPerRow: number } | null;
  locked: boolean;
  preview: BlockLayout | null;
  onPreview: (layout: BlockLayout | null) => void;
  onSaved: (message: string) => Promise<void>;
}) {
  const [rows, setRows] = useState(String(block.row_count || suggested?.rows || 10));
  const [seatsPerRow, setSeatsPerRow] = useState(String(block.seats_per_row || suggested?.seatsPerRow || 20));
  const [scheme, setScheme] = useState<Schemas["RowLabelScheme"]>("ALPHA");
  const [direction, setDirection] = useState<Schemas["NumberingDirection"]>(block.numbering_direction);
  const [offsets, setOffsets] = useState("");
  const [local, setLocal] = useState<Partial<Record<SeatField, string>>>({});
  const [confirming, setConfirming] = useState<Schemas["GenerateSeats"] | null>(null);
  const fixed = block.has_inventory;

  const generate = useMutation({
    mutationFn: (body: Schemas["GenerateSeats"]) => generateSeats(block.id, body),
    onSuccess: (layout) => {
      if (layout.dry_run) {
        onPreview(layout);
        return;
      }
      setConfirming(null);
      return onSaved(`Block ${block.code} now has ${formatCount(layout.seat_count)} seats in ${layout.rows.length} rows.`);
    },
  });

  function read(): Schemas["GenerateSeats"] | null {
    const errors: Partial<Record<SeatField, string>> = {};
    const rowCount = readWhole(rows);
    if (rowCount === null || rowCount < 1 || rowCount > 200) errors.rows = "Enter a whole number of rows from 1 to 200.";
    const perRow = readWhole(seatsPerRow);
    if (perRow === null || perRow < 1 || perRow > 500) errors.seats_per_row = "Enter a whole number of seats from 1 to 500.";
    let rowOffsets: number[] | undefined;
    if (offsets.trim()) {
      const parts = offsets.split(/[,\s]+/).filter(Boolean).map(readNumber);
      if (parts.some((part) => part === null)) errors.row_offsets = "Use numbers separated by commas, for example 0, 2.5, 0, 2.5.";
      else if (rowCount !== null && parts.length !== rowCount) errors.row_offsets = `Give one number for each row: ${rowCount} needed, ${parts.length} given. Or leave this empty.`;
      else rowOffsets = parts as number[];
    }
    setLocal(errors);
    if (Object.keys(errors).length > 0 || rowCount === null || perRow === null) return null;
    // The row names and the numbering side are what the two boxes show, so they are always sent.
    return {
      rows: rowCount,
      seats_per_row: perRow,
      row_label_scheme: scheme,
      numbering_direction: direction,
      ...(rowOffsets ? { row_offsets: rowOffsets } : {}),
      dry_run: true,
    };
  }

  function changed() {
    setLocal({});
    onPreview(null);
    if (generate.isError) generate.reset();
  }

  function stagger() {
    const rowCount = readWhole(rows);
    if (rowCount === null) return;
    const half = show(block.seat_pitch_x / 2);
    setOffsets(Array.from({ length: rowCount }, (_unused, index) => (index % 2 === 0 ? "0" : half)).join(", "));
    changed();
  }

  const { fields, other } = splitFieldErrors(generate.error, SEAT_FIELDS);
  const errorFor = (field: SeatField) => local[field] ?? fields[field] ?? null;
  const inventory = isCode(generate.error, "BLOCK_HAS_INVENTORY");
  const formError =
    generate.error && Object.keys(fields).length === 0 ? (inventory ? inventoryMessage(generate.error, "regenerated") : describeError(generate.error, { NOT_FOUND: "This block no longer exists. Reload the page." })) : null;
  const disabled = locked || fixed;

  return (
    <Card>
      <div className="card__header">
        <h2>Step 2: {block.seat_count === 0 ? "generate seats" : "regenerate seats"}</h2>
      </div>
      <form
        className="card__body stack"
        noValidate
        onSubmit={(event) => {
          event.preventDefault();
          if (generate.isPending || disabled) return;
          const body = read();
          if (body) generate.mutate(body);
        }}
      >
        {fixed && <p className="small muted">These seats are on sale for a published event, so they cannot be regenerated.</p>}
        <fieldset className="formgrid formgrid--tight" disabled={disabled}>
          <TextField label="Rows" name="rows" inputMode="numeric" maxLength={3} value={rows} onChange={(e) => { setRows(e.target.value); changed(); }} error={errorFor("rows")} />
          <TextField label="Seats in each row" name="seats_per_row" inputMode="numeric" maxLength={3} value={seatsPerRow} onChange={(e) => { setSeatsPerRow(e.target.value); changed(); }} error={errorFor("seats_per_row")} />
          <SelectField label="Row names" name="row_label_scheme" value={scheme} onChange={(e) => { setScheme(e.target.value as Schemas["RowLabelScheme"]); changed(); }} error={errorFor("row_label_scheme")} hint={scheme === "ALPHA" ? "Seats are named like C14." : "Seats are named like 12-14."}>
            <option value="ALPHA">Letters: A, B, C … Z, AA</option>
            <option value="NUMERIC">Numbers: 1, 2, 3 …</option>
          </SelectField>
          <SelectField label="Seat 1 is on the" name="numbering_direction" value={direction} onChange={(e) => { setDirection(e.target.value as Schemas["NumberingDirection"]); changed(); }} error={errorFor("numbering_direction")} hint="Looking at the block from the pitch.">
            <option value="LTR">Left</option>
            <option value="RTL">Right</option>
          </SelectField>
          <div className="formgrid__wide">
            <TextField
              label="Shift rows sideways (optional)"
              name="row_offsets"
              placeholder="0, 2.5, 0, 2.5"
              value={offsets}
              onChange={(e) => { setOffsets(e.target.value); changed(); }}
              error={errorFor("row_offsets")}
              hint={
                <>
                  One number per row, front row first, in plan units. Empty means no shift.{" "}
                  <button type="button" className="link-button" onClick={stagger}>
                    Stagger every second row by half a seat
                  </button>
                </>
              }
            />
          </div>
        </fieldset>

        {(formError || other.length > 0) && (
          <Alert tone="error">
            {formError}
            {other.map((message) => (
              <p key={message}>{message}</p>
            ))}
          </Alert>
        )}
        {preview && (
          <Alert tone="info" title={`Preview: ${formatCount(preview.seat_count)} seats in ${preview.rows.length} rows`}>
            Drawn on the plan in outline. Nothing is saved yet. Rows {preview.rows[0]?.label} to {preview.rows[preview.rows.length - 1]?.label}.
          </Alert>
        )}

        <div className="form-actions">
          <Button type="submit" variant="secondary" loading={generate.isPending && generate.variables?.dry_run} disabled={disabled}>
            Preview on the plan
          </Button>
          <Button
            disabled={disabled || !preview || generate.isPending}
            loading={generate.isPending && !generate.variables?.dry_run && !confirming}
            onClick={() => {
              const body = read();
              if (!body) return;
              // Without dry_run the server saves.
              const { dry_run: _preview, ...real } = body;
              if (block.seat_count > 0) setConfirming(real);
              else generate.mutate(real);
            }}
          >
            Save these seats
          </Button>
          {!preview && !disabled && <span className="small muted">Preview first, then save.</span>}
        </div>
      </form>

      <ConfirmDialog
        open={confirming !== null}
        title={`Replace the seats of block ${block.code}?`}
        confirmLabel={generate.isPending ? "Replacing…" : "Replace seats"}
        cancelLabel="Keep the old seats"
        danger
        busy={generate.isPending}
        error={generate.error && confirming ? (formError ?? describeError(generate.error)) : null}
        onConfirm={() => confirming && generate.mutate(confirming)}
        onCancel={() => { setConfirming(null); generate.reset(); }}
      >
        <p>
          The block's {formatCount(block.seat_count)} seats are thrown away and {confirming ? formatCount(confirming.rows * confirming.seats_per_row) : ""} new ones
          are made. Draft events keep the block's category, but rows that were painted differently lose that.
        </p>
      </ConfirmDialog>
    </Card>
  );
}

function TemplatesCard({ venue }: { venue: VenueDetail }) {
  const queryClient = useQueryClient();
  const templates = useTemplates(venue.id);
  const [toDelete, setToDelete] = useState<Template | null>(null);
  const [deletedName, setDeletedName] = useState<string | null>(null);

  const remove = useMutation({
    mutationFn: async (template: Template) => {
      try {
        await unwrap(
          api.DELETE("/admin/venues/{venueId}/templates/{templateId}", { params: { path: { venueId: venue.id, templateId: template.id } } }),
        );
      } catch (error) {
        // Already gone (deleted in another tab, or by a colleague): that is what was asked for.
        if (!isCode(error, "NOT_FOUND")) throw error;
      }
      return template;
    },
    onSuccess: (template) => {
      setToDelete(null);
      setDeletedName(template.name);
    },
    onSettled: () => queryClient.invalidateQueries({ queryKey: keys.templates(venue.id) }),
  });

  const events = useAdminEvents("ALL", 100);
  const publishing = usePublishInFlight();
  const [eventChoice, setEventChoice] = useState<number | null>(null);
  const candidates = (events.data?.pages.flatMap((page) => page.items) ?? []).filter((event) => event.venue_id === venue.id);
  const eventId = eventChoice ?? candidates[0]?.id;

  return (
    <Card>
      <div className="card__header">
        <h2>Venue templates</h2>
        <span className="small muted">A template is a saved set of categories, prices and painting</span>
      </div>
      <div className="card__body card__body--flush">
        {templates.isPending ? (
          <SkeletonRows label="Loading templates" />
        ) : templates.isError ? (
          <LoadFailed message={describeError(templates.error)} onRetry={() => templates.refetch()} retrying={templates.isFetching} />
        ) : templates.data.items.length === 0 ? (
          <StateBlock title="No templates yet">
            <p>Paint one event, then save it here so the next event takes minutes.</p>
          </StateBlock>
        ) : (
          <div className="table-wrap">
            <table className="table">
              <thead>
                <tr>
                  <th scope="col">Template</th>
                  <th scope="col">Categories</th>
                  <th scope="col">Painting</th>
                  <th scope="col">Saved</th>
                  <th scope="col">
                    <span className="visually-hidden">Actions</span>
                  </th>
                </tr>
              </thead>
              <tbody>
                {templates.data.items.map((template) => (
                  <tr key={template.id}>
                    <td className="table__primary" data-label="Template">
                      {template.name}
                    </td>
                    <td className="table__wrap" data-label="Categories">
                      <span className="catlist">
                        {template.categories.map((category) => (
                          <span key={category.name} className="catlist__item">
                            <Swatch color={category.color_hex} />
                            {category.name} · {formatKes(category.price_cents)}
                          </span>
                        ))}
                      </span>
                    </td>
                    <td data-label="Painting">
                      {formatCount(template.block_assignment_count)} blocks, {formatCount(template.row_assignment_count)} row overrides
                    </td>
                    <td data-label="Saved">
                      <time dateTime={template.created_at}>{formatDate(template.created_at)}</time>
                    </td>
                    <td className="table__actions-cell" data-label="Actions">
                      <div className="table__actions">
                        <Button
                          variant="danger"
                          size="small"
                          onClick={() => {
                            remove.reset();
                            setDeletedName(null);
                            setToDelete(template);
                          }}
                          aria-label={`Delete the template ${template.name}`}
                        >
                          Delete
                        </Button>
                      </div>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </div>
      {deletedName !== null && (
        <div className="card__body">
          <Alert tone="success">The template “{deletedName}” is deleted.</Alert>
        </div>
      )}
      <ConfirmDialog
        open={toDelete !== null}
        title="Delete this template?"
        confirmLabel={remove.isPending ? "Deleting…" : remove.isError ? "Try again" : "Delete template"}
        cancelLabel="Keep it"
        danger
        busy={remove.isPending}
        error={remove.error ? describeError(remove.error) : null}
        onConfirm={() => toDelete && remove.mutate(toDelete)}
        onCancel={() => setToDelete(null)}
      >
        <p>
          The template <strong>{toDelete?.name}</strong> will no longer be offered when you create an event.
        </p>
        <p>
          Events already created from it keep their categories, prices and painting. <strong>This cannot be undone.</strong>
        </p>
      </ConfirmDialog>
      <div className="card__footer stack">
        <h3 className="subhead">Save an event as a new template</h3>
        {events.isError ? (
          <Alert tone="warning">
            The events did not load ({describeError(events.error)}).{" "}
            <button type="button" className="link-button" onClick={() => events.refetch()}>
              Try again
            </button>
          </Alert>
        ) : candidates.length === 0 && !events.isPending ? (
          <p className="small muted">There is no event at this venue to copy from yet.</p>
        ) : (
          <SaveTemplateForm venueId={venue.id} eventId={eventId} disabledReason={publishing ? PUBLISHING_NOTE : null}>
            <SelectField label="Copy categories and painting from" name="event_id" value={eventId ?? ""} onChange={(e) => setEventChoice(Number(e.target.value))}>
              {candidates.map((event) => (
                <option key={event.id} value={event.id}>
                  {event.title}
                </option>
              ))}
            </SelectField>
          </SaveTemplateForm>
        )}
      </div>
    </Card>
  );
}
