package diff

import (
	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/schema"
)

// A view's body is engine text and compares as the text it is (ADR 0065), so two engines' renderings
// of one view differ and a diff across engines proposes replacing every view it can see. That is the
// model's own honesty rather than this differ's choice: nothing here can know that an expanded column
// list and a SELECT * are the same query, and claiming otherwise is what a snapshot must not do.

// planViews emits what the two schemas' views differ by, and runs last.
func (d *differ) planViews() {
	for _, view := range d.from.Views {
		if _, kept := d.to.View(view.Name); !kept {
			d.dropView(view)
		}
	}

	for _, view := range d.to.Views {
		before, existed := d.from.View(view.Name)
		if !existed {
			d.createView(view)

			continue
		}

		if before.Equal(view) {
			continue
		}

		d.replaceView(before, view)
	}
}

// createView emits a view the target schema holds and this one does not.
func (d *differ) createView(view schema.View) {
	if marked := markedBeyond(view.Beyond); marked != "" {
		d.refuse(ir.KindCreateView, view.Name, marked)

		return
	}

	d.emit(ir.CreateView{View: view.Name, Query: view.Query})
}

// dropView emits a view this schema holds and the target does not, recording its query.
func (d *differ) dropView(view schema.View) {
	if marked := markedBeyond(view.Beyond); marked != "" {
		d.refuse(ir.KindDropView, view.Name, marked)

		return
	}

	d.emit(ir.DropView{View: view.Name, From: &ir.ViewDef{Query: view.Query}})
}

// replaceView emits a view whose body the target schema states differently, recording the old one.
func (d *differ) replaceView(before, after schema.View) {
	if marked := markedBeyond(before.Beyond); marked != "" {
		d.refuse(ir.KindCreateView, after.Name, marked)

		return
	}

	if marked := markedBeyond(after.Beyond); marked != "" {
		d.refuse(ir.KindCreateView, after.Name, marked)

		return
	}

	d.emit(ir.CreateView{View: after.Name, Query: after.Query, OrReplace: true,
		From: &ir.ViewDef{Query: before.Query}})
}
