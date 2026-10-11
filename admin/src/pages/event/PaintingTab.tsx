import { unwrap } from "@talanta/shared";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useMemo, useState } from "react";
import { Link } from "react-router-dom";
import { BlockSeats } from "../../components/BlockSeats";
import { SaveTemplateForm } from "../../components/SaveTemplateForm";
import { StadiumPlan } from "../../components/StadiumPlan";
import { Alert, Button, Card, LoadFailed, SkeletonRows, StateBlock, Swatch } from "../../components/ui";
import { api } from "../../lib/api";
import { describeError } from "../../lib/errors";
import { blocksInBox, type Box } from "../../lib/geometry";
import { formatCount } from "../../lib/money";
import { keys, useAssignments, useBlockLayout, useCategories, useVenue, type Assignments, type Block, type Category } from "../../lib/queries";
import { editLock, useEventContext } from "./EventPage";

interface PaintRequest {
  categoryId: number | null;
  blockIds: number[];
  rowIds: number[];
}

/** What the last paint replaced, so it can be put back once. */
interface Undo {
  label: string;
  blocks: Map<number, number | null>;
  rows: Map<number, number | null>;
}

function usePainting(eventId: number) {
  const queryClient = useQueryClient();
  const [undo, setUndo] = useState<Undo | null>(null);

  const send = (request: PaintRequest) =>
    unwrap(
      api.POST("/admin/events/{eventId}/assign-category", {
        params: { path: { eventId } },
        body: { category_id: request.categoryId, block_ids: request.blockIds, row_ids: request.rowIds },
      }),
    );
  const settle = (assignments?: Assignments) => {
    if (assignments) queryClient.setQueryData(keys.assignments(eventId), assignments);
    return Promise.all([
      queryClient.invalidateQueries({ queryKey: keys.readiness(eventId) }),
      assignments ? undefined : queryClient.invalidateQueries({ queryKey: keys.assignments(eventId) }),
    ]);
  };

  const paint = useMutation({
    mutationFn: async (request: PaintRequest & { label: string }) => {
      const before = queryClient.getQueryData<Assignments>(keys.assignments(eventId));
      const previous: Undo = {
        label: request.label,
        blocks: new Map(request.blockIds.map((id) => [id, before?.blocks.find((entry) => entry.block_id === id)?.category_id ?? null])),
        rows: new Map(request.rowIds.map((id) => [id, before?.rows.find((entry) => entry.row_id === id)?.category_id ?? null])),
      };
      const after = await send(request);
      return { after, previous };
    },
    onSuccess: ({ after, previous }) => {
      setUndo(previous);
      return settle(after);
    },
    onError: () => settle(),
  });

  const revert = useMutation({
    mutationFn: async (previous: Undo) => {
      // One request per category that was there before (null = nothing was there).
      const groups = new Map<number | null, PaintRequest>();
      const group = (categoryId: number | null) => {
        if (!groups.has(categoryId)) groups.set(categoryId, { categoryId, blockIds: [], rowIds: [] });
        return groups.get(categoryId)!;
      };
      previous.blocks.forEach((categoryId, id) => group(categoryId).blockIds.push(id));
      previous.rows.forEach((categoryId, id) => group(categoryId).rowIds.push(id));
      let last: Assignments | undefined;
      for (const request of groups.values()) last = await send(request);
      return last;
    },
    onSuccess: (after) => {
      setUndo(null);
      return settle(after);
    },
    onError: () => settle(),
  });

  return { paint, revert, undo, clearUndo: () => setUndo(null) };
}

export function PaintingTab() {
  const context = useEventContext();
  const { event } = context;
  const lock = editLock(context, "painting");
  const venue = useVenue(event.venue_id);
  const categories = useCategories(event.id);
  const assignments = useAssignments(event.id);
  const { paint, revert, undo } = usePainting(event.id);
  const [selected, setSelected] = useState<ReadonlySet<number>>(new Set());
  const [openBlockId, setOpenBlockId] = useState<number | null>(null);
  const [touchDrag, setTouchDrag] = useState(false);

  const blockCategory = useMemo(
    () => new Map(assignments.data?.blocks.map((entry) => [entry.block_id, entry.category_id])),
    [assignments.data],
  );
  const overridesByBlock = useMemo(() => {
    const map = new Map<number, number>();
    for (const entry of assignments.data?.rows ?? []) map.set(entry.block_id, (map.get(entry.block_id) ?? 0) + 1);
    return map;
  }, [assignments.data]);
  const categoryById = useMemo(() => new Map(categories.data?.items.map((category) => [category.id, category])), [categories.data]);
  const allBlocks = useMemo(() => venue.data?.stands.flatMap((stand) => stand.blocks) ?? [], [venue.data]);

  if (venue.isPending || categories.isPending || assignments.isPending) {
    return (
      <Card>
        <div className="card__body card__body--flush">
          <SkeletonRows label="Loading the stadium plan" rows={5} />
        </div>
      </Card>
    );
  }
  const failed = venue.error ?? categories.error ?? assignments.error;
  if (failed || !venue.data || !categories.data || !assignments.data) {
    return (
      <Card>
        <LoadFailed
          message={describeError(failed)}
          onRetry={() => {
            void venue.refetch();
            void categories.refetch();
            void assignments.refetch();
          }}
          retrying={venue.isFetching || categories.isFetching || assignments.isFetching}
        />
      </Card>
    );
  }

  const cats = categories.data.items;
  const seated = allBlocks.filter((block) => block.seat_count > 0);
  const unassigned = seated.filter((block) => !blockCategory.has(block.id));
  const selectedBlocks = allBlocks.filter((block) => selected.has(block.id));
  const selectedSeats = selectedBlocks.reduce((sum, block) => sum + block.seat_count, 0);
  const openBlock = openBlockId === null ? null : (allBlocks.find((block) => block.id === openBlockId) ?? null);
  const busy = paint.isPending || revert.isPending;
  const readOnly = Boolean(lock);

  // A click always toggles one block, so several can be picked with a mouse, a finger or the keyboard alike.
  function toggle(block: Block) {
    setSelected((current) => {
      const next = new Set(current);
      if (next.has(block.id)) next.delete(block.id);
      else next.add(block.id);
      return next;
    });
  }

  function boxSelect(box: Box, additive: boolean) {
    const hit = blocksInBox(seated, box);
    setSelected((current) => new Set(additive ? [...current, ...hit] : hit));
  }

  function paintBlocks(category: Category | null) {
    if (selectedBlocks.length === 0 || busy) return;
    paint.mutate({
      categoryId: category?.id ?? null,
      blockIds: selectedBlocks.map((block) => block.id),
      rowIds: [],
      label: `${selectedBlocks.length} ${selectedBlocks.length === 1 ? "block" : "blocks"} ${category ? `painted ${category.name}` : "cleared"}`,
    });
  }

  const paintError = (paint.error ?? revert.error)
    ? describeError(paint.error ?? revert.error, {
        EVENT_NOT_EDITABLE: "This event is no longer a draft, so its painting cannot be changed. Reload the page to see its current state.",
        NOT_FOUND: "That category no longer exists. Reload the page.",
        VENUE_MISMATCH: "One of those blocks or rows is not part of this event's venue. Reload the page.",
        INTERNAL: "The server could not save that. If the event is being published, wait for it to finish, then reload.",
      })
    : null;

  return (
    <>
      {lock && <Alert tone="info">{lock}</Alert>}
      {cats.length === 0 && !readOnly && (
        <Alert tone="warning" title="There are no categories to paint with">
          <Link to="../pricing" relative="path">
            Add categories and prices
          </Link>{" "}
          first, then come back and paint the blocks.
        </Alert>
      )}

      <Card>
        <div className="card__header">
          <h2>{readOnly ? "Seat categories" : "Paint categories onto blocks"}</h2>
          <span className="small muted">
            {formatCount(seated.length - unassigned.length)} of {formatCount(seated.length)} blocks painted
          </span>
        </div>
        <div className="card__body stack">
          {!readOnly && (
            <p className="small muted">
              Click blocks to pick or unpick them, or drag a box across several (hold Shift while dragging to add to what is
              picked). With the keyboard, Tab moves between blocks and Space picks one. Then choose a category below the plan.
            </p>
          )}

          <ul className="legend" aria-label="Categories">
            {cats.map((category) => (
              <li key={category.id}>
                <Swatch color={category.color_hex} />
                {category.name}
                <span className="muted">
                  {" "}
                  {formatCount(assignments.data.blocks.filter((entry) => entry.category_id === category.id).length)}
                </span>
              </li>
            ))}
            <li>
              <span className="swatch swatch--none" aria-hidden="true" />
              No category <span className="muted">{formatCount(unassigned.length)}</span>
            </li>
            <li>
              <span className="legend__dot" aria-hidden="true" />
              Has rows painted differently
            </li>
          </ul>

          {unassigned.length > 0 && (
            <Alert tone="warning" title={`${formatCount(unassigned.length)} ${unassigned.length === 1 ? "block has" : "blocks have"} no category`}>
              {unassigned
                .slice(0, 12)
                .map((block) => block.code)
                .join(", ")}
              {unassigned.length > 12 ? ` and ${unassigned.length - 12} more` : ""}. They are hatched on the plan. An event
              cannot be published while any seat has no category.{" "}
              {!readOnly && (
                <button type="button" className="link-button" onClick={() => setSelected(new Set(unassigned.map((block) => block.id)))}>
                  Pick them all
                </button>
              )}
            </Alert>
          )}

          <div className="planwrap">
            <StadiumPlan
              venue={venue.data}
              label={`Plan of ${venue.data.name}. ${seated.length} blocks.`}
              selected={selected}
              touchDrag={touchDrag}
              onBlockPress={toggle}
              onBoxDrag={boxSelect}
              look={(block, stand) => {
                const category = categoryById.get(blockCategory.get(block.id) ?? -1);
                const overrides = overridesByBlock.get(block.id) ?? 0;
                return {
                  fill: block.seat_count === 0 ? "#ffffff" : (category?.color_hex ?? null),
                  marked: overrides > 0,
                  title: `Block ${block.code}, ${stand.name} ${stand.tier.toLowerCase()}, ${formatCount(block.seat_count)} seats, ${
                    block.seat_count === 0 ? "no seats yet" : (category?.name ?? "no category")
                  }${overrides > 0 ? `, ${overrides} rows painted differently` : ""}`,
                };
              }}
            />
          </div>

          <div className="row">
            <label className="toggle">
              <input type="checkbox" checked={touchDrag} onChange={(e) => setTouchDrag(e.target.checked)} />
              Drag a box with my finger (turn off to scroll the page)
            </label>
          </div>

          <div className="paintbar" role="region" aria-label="Paint the picked blocks" aria-live="polite">
            <p className="paintbar__summary">
              {selectedBlocks.length === 0 ? (
                <span className="muted">No blocks picked.</span>
              ) : (
                <>
                  <strong>
                    {formatCount(selectedBlocks.length)} {selectedBlocks.length === 1 ? "block" : "blocks"}
                  </strong>{" "}
                  picked, {formatCount(selectedSeats)} seats
                  <span className="muted">
                    {" "}
                    (
                    {selectedBlocks
                      .slice(0, 8)
                      .map((block) => block.code)
                      .join(", ")}
                    {selectedBlocks.length > 8 ? "…" : ""})
                  </span>
                </>
              )}
            </p>
            <div className="paintbar__actions">
              {!readOnly &&
                cats.map((category) => (
                  <Button
                    key={category.id}
                    variant="secondary"
                    size="small"
                    disabled={selectedBlocks.length === 0 || busy}
                    onClick={() => paintBlocks(category)}
                    aria-label={`Paint ${category.name}`}
                  >
                    <Swatch color={category.color_hex} />
                    {category.name}
                  </Button>
                ))}
              {!readOnly && (
                <Button variant="ghost" size="small" disabled={selectedBlocks.length === 0 || busy} onClick={() => paintBlocks(null)}>
                  Remove category
                </Button>
              )}
              <Button
                variant="ghost"
                size="small"
                disabled={selectedBlocks.length !== 1 || selectedBlocks[0]!.seat_count === 0}
                onClick={() => setOpenBlockId(selectedBlocks[0]!.id)}
              >
                {selectedBlocks.length === 1 ? `Open block ${selectedBlocks[0]!.code}` : "Open block"}
              </Button>
              {selectedBlocks.length > 0 && (
                <Button variant="ghost" size="small" onClick={() => setSelected(new Set())}>
                  Unpick all
                </Button>
              )}
            </div>
          </div>

          {paintError && <Alert tone="error">{paintError}</Alert>}
          {undo && !readOnly && (
            <div className="undo" role="status">
              <span>Done: {undo.label}.</span>
              <Button variant="ghost" size="small" loading={revert.isPending} disabled={paint.isPending} onClick={() => revert.mutate(undo)}>
                Undo
              </Button>
            </div>
          )}
        </div>
      </Card>

      {openBlock && (
        <BlockRows
          key={openBlock.id}
          block={openBlock}
          categories={cats}
          blockCategoryId={blockCategory.get(openBlock.id) ?? null}
          overrides={assignments.data.rows.filter((entry) => entry.block_id === openBlock.id)}
          readOnly={readOnly}
          busy={busy}
          touchDrag={touchDrag}
          onPaint={(category, rowIds, labels) =>
            paint.mutate({
              categoryId: category?.id ?? null,
              blockIds: [],
              rowIds,
              label: category
                ? `${rowIds.length === 1 ? `row ${labels[0]}` : `${rowIds.length} rows`} of ${openBlock.code} painted ${category.name}`
                : `${rowIds.length === 1 ? `row ${labels[0]}` : `${rowIds.length} rows`} of ${openBlock.code} set back to the block's category`,
            })
          }
          onClose={() => setOpenBlockId(null)}
        />
      )}

      {event.status !== "CANCELLED" ? (
        <Card>
          <div className="card__header">
            <h2>Save this painting as a template</h2>
          </div>
          <div className="card__body stack">
            <p className="small muted">
              Saves this event's categories, prices and painting under a name. The next event at {venue.data.name} can start
              from it.
            </p>
            <SaveTemplateForm
              venueId={event.venue_id}
              eventId={event.id}
              disabledReason={context.publishing ? "Wait until publishing has finished." : null}
            />
          </div>
        </Card>
      ) : null}
    </>
  );
}

function BlockRows({
  block,
  categories,
  blockCategoryId,
  overrides,
  readOnly,
  busy,
  touchDrag,
  onPaint,
  onClose,
}: {
  block: Block;
  categories: Category[];
  blockCategoryId: number | null;
  overrides: Assignments["rows"];
  readOnly: boolean;
  busy: boolean;
  touchDrag: boolean;
  onPaint: (category: Category | null, rowIds: number[], labels: string[]) => void;
  onClose: () => void;
}) {
  const layout = useBlockLayout(block.id);
  const [picked, setPicked] = useState<ReadonlySet<number>>(new Set());
  const categoryById = new Map(categories.map((category) => [category.id, category]));
  const overrideByRow = new Map(overrides.map((entry) => [entry.row_id, entry.category_id]));
  const blockCategory = blockCategoryId === null ? undefined : categoryById.get(blockCategoryId);

  const rows = layout.data?.rows ?? [];
  const categoryOfRow = (rowIndex: number) => {
    const row = rows[rowIndex];
    const override = row?.id != null ? overrideByRow.get(row.id) : undefined;
    return override !== undefined ? categoryById.get(override) : blockCategory;
  };
  const pickedRows = rows.filter((_row, index) => picked.has(index));
  const pickedIds = pickedRows.flatMap((row) => (row.id == null ? [] : [row.id]));

  function selectRows(indexes: number[], mode: "toggle" | "replace" | "add") {
    setPicked((current) => {
      if (mode === "replace") return new Set(indexes);
      const next = new Set(current);
      for (const index of indexes) {
        if (mode === "toggle" && next.has(index)) next.delete(index);
        else next.add(index);
      }
      return next;
    });
  }

  function paintRows(category: Category | null) {
    if (pickedIds.length === 0) return;
    onPaint(category, pickedIds, pickedRows.map((row) => row.label));
    setPicked(new Set());
  }

  return (
    <Card>
      <div className="card__header">
        <h2>
          Block {block.code}: rows
        </h2>
        <Button variant="ghost" size="small" onClick={onClose}>
          Close block
        </Button>
      </div>
      {layout.isPending ? (
        <div className="card__body card__body--flush">
          <SkeletonRows label="Loading the block's seats" />
        </div>
      ) : layout.isError ? (
        <LoadFailed message={describeError(layout.error)} onRetry={() => layout.refetch()} retrying={layout.isFetching} />
      ) : rows.length === 0 ? (
        <StateBlock title="This block has no seats yet">
          <p>
            Generate its seats under <Link to="/layout">Venue layout</Link> first.
          </p>
        </StateBlock>
      ) : (
        <div className="card__body stack">
          <p className="small muted">
            {formatCount(layout.data.seat_count)} seats in {rows.length} rows, front row first. The block is painted{" "}
            <strong>{blockCategory?.name ?? "with no category"}</strong>.{" "}
            {readOnly
              ? "Rows painted differently are listed below."
              : "Pick rows (click them, drag a box, or tick them in the list) to give them a different category."}
          </p>
          <div className="blockview">
            <div className="blockview__plan">
              <BlockSeats
                layout={layout.data}
                label={`Seats of block ${block.code}, ${rows.length} rows`}
                rowFill={(index) => categoryOfRow(index)?.color_hex ?? null}
                selectedRows={picked}
                onSelectRows={readOnly ? undefined : selectRows}
                touchDrag={touchDrag}
              />
            </div>
            <div className="blockview__rows">
              <table className="rowtable">
                <thead>
                  <tr>
                    <th scope="col">Row</th>
                    <th scope="col">Seats</th>
                    <th scope="col">Category</th>
                  </tr>
                </thead>
                <tbody>
                  {rows.map((row, index) => {
                    const category = categoryOfRow(index);
                    const overridden = row.id != null && overrideByRow.has(row.id);
                    return (
                      <tr key={row.row_index} data-picked={picked.has(index) || undefined}>
                        <td>
                          <label className="rowtable__pick">
                            {!readOnly && (
                              <input
                                type="checkbox"
                                checked={picked.has(index)}
                                onChange={() => selectRows([index], "toggle")}
                                aria-label={`Pick row ${row.label}`}
                              />
                            )}
                            {row.label}
                          </label>
                        </td>
                        <td>{row.seats.length}</td>
                        <td>
                          <span className="catname">
                            {category ? <Swatch color={category.color_hex} /> : <span className="swatch swatch--none" aria-hidden="true" />}
                            {category?.name ?? "None"}
                            {overridden && <span className="chip chip--plain chip--role">Row only</span>}
                          </span>
                        </td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            </div>
          </div>

          {!readOnly && (
            <div className="paintbar" role="region" aria-label="Paint the picked rows" aria-live="polite">
              <p className="paintbar__summary">
                {pickedRows.length === 0 ? (
                  <span className="muted">No rows picked.</span>
                ) : (
                  <>
                    <strong>
                      {pickedRows.length} {pickedRows.length === 1 ? "row" : "rows"}
                    </strong>{" "}
                    picked ({pickedRows.map((row) => row.label).join(", ")})
                  </>
                )}
              </p>
              <div className="paintbar__actions">
                {categories.map((category) => (
                  <Button
                    key={category.id}
                    variant="secondary"
                    size="small"
                    disabled={pickedIds.length === 0 || busy}
                    onClick={() => paintRows(category)}
                    aria-label={`Paint rows ${category.name}`}
                  >
                    <Swatch color={category.color_hex} />
                    {category.name}
                  </Button>
                ))}
                <Button variant="ghost" size="small" disabled={pickedIds.length === 0 || busy} onClick={() => paintRows(null)}>
                  Same as the block
                </Button>
              </div>
            </div>
          )}
        </div>
      )}
    </Card>
  );
}
