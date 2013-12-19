package index

import (
	"errors"
	. "pilosa/util"
	"time"

	"github.com/golang/groupcache/lru"
)

type FragmentContainer struct {
	fragments map[SUUID]*Fragment
}

func NewFragmentContainer() *FragmentContainer {
	return &FragmentContainer{make(map[SUUID]*Fragment)}
}

type BitmapHandle uint64

func (self *FragmentContainer) GetFragment(frag_id SUUID) (*Fragment, bool) {
	//lock
	c, v := self.fragments[frag_id]
	return c, v
}

func (self *FragmentContainer) Empty(frag_id SUUID) (BitmapHandle, error) {
	if fragment, found := self.GetFragment(frag_id); found {
		request := NewEmpty()
		fragment.requestChan <- request
		return request.Response().answer.(BitmapHandle), nil
	}
	return 0, errors.New("Invalid Bitmap Handle")
}

func (self *FragmentContainer) Intersect(frag_id SUUID, bh []BitmapHandle) (BitmapHandle, error) {
	if fragment, found := self.GetFragment(frag_id); found {
		request := NewIntersect(bh)
		fragment.requestChan <- request
		return request.Response().answer.(BitmapHandle), nil
	}
	return 0, errors.New("Invalid Bitmap Handle")
}
func (self *FragmentContainer) Union(frag_id SUUID, bh []BitmapHandle) (BitmapHandle, error) {
	if fragment, found := self.GetFragment(frag_id); found {
		request := NewUnion(bh)
		fragment.requestChan <- request
		return request.Response().answer.(BitmapHandle), nil
	}
	return 0, errors.New("Invalid Bitmap Handle")
}

func (self *FragmentContainer) Get(frag_id SUUID, bitmap_id uint64) (BitmapHandle, error) {
	if fragment, found := self.GetFragment(frag_id); found {
		request := NewGet(bitmap_id)
		fragment.requestChan <- request
		return request.Response().answer.(BitmapHandle), nil
	}
	return 0, errors.New("Invalid Bitmap Handle")
}
func (self *FragmentContainer) Count(frag_id SUUID, bitmap BitmapHandle) (uint64, error) {
	if fragment, found := self.GetFragment(frag_id); found {
		request := NewCount(bitmap)
		fragment.requestChan <- request
		return request.Response().answer.(uint64), nil
	}
	return 0, errors.New("Invalid Bitmap Handle")
}

func (self *FragmentContainer) GetBytes(frag_id SUUID, bh BitmapHandle) ([]byte, error) {
	if fragment, found := self.GetFragment(frag_id); found {
		request := NewGetBytes(bh)
		fragment.requestChan <- request
		return request.Response().answer.([]byte), nil
	}
	return nil, errors.New("Invalid Bitmap Handle")
}

func (self *FragmentContainer) FromBytes(frag_id SUUID, bytes []byte) (BitmapHandle, error) {
	if fragment, found := self.GetFragment(frag_id); found {
		request := NewFromBytes(bytes)
		fragment.requestChan <- request
		return request.Response().answer.(BitmapHandle), nil
	}
	return 0, errors.New("Invalid Bitmap Handle")
}

func (self *FragmentContainer) SetBit(frag_id SUUID, bitmap_id uint64, pos uint64) (bool, error) {
	if fragment, found := self.GetFragment(frag_id); found {
		request := NewSetBit(bitmap_id, pos)
		fragment.requestChan <- request
		return request.Response().answer.(bool), nil
	}
	return false, errors.New("Invalid Bitmap Handle")
}

func (self *FragmentContainer) AddFragment(frame string, db string, slice int, id SUUID) {
	f := NewFragment(id, db, slice, frame)
	self.fragments[id] = f
	go f.ServeFragment()
}

type Pilosa interface {
	Get(id uint64) IBitmap
	SetBit(id uint64, bit_pos uint64) bool
}

type Fragment struct {
	requestChan chan Command
	fragment_id SUUID
	impl        Pilosa
	counter     uint64
	slice       int
	cache       *lru.Cache
	mesg_count  uint64
	mesg_time   time.Duration
}

func NewFragment(frag_id SUUID, db string, slice int, frame string) *Fragment {
	f := new(Fragment)
	f.requestChan = make(chan Command, 64)
	f.fragment_id = frag_id
	f.cache = lru.New(10000)
	f.impl = NewGeneral(db, slice, NewMemoryStorage())
	f.slice = slice
	return f
}

func (self *Fragment) getBitmap(bitmap BitmapHandle) (IBitmap, bool) {
	bm, ok := self.cache.Get(bitmap)
	return bm.(IBitmap), ok
}

func (self *Fragment) NewHandle(bitmap_id uint64) BitmapHandle {
	bm := self.impl.Get(bitmap_id)
	return self.AllocHandle(bm)
	//given a bitmap_id return a newly allocated  handle
}
func (self *Fragment) AllocHandle(bm IBitmap) BitmapHandle {
	handle := self.nextHandle()
	self.cache.Add(handle, bm)
	return handle
}

func (self *Fragment) nextHandle() BitmapHandle {
	millis := uint64(time.Now().UTC().UnixNano())
	id := millis << (64 - 41)
	id |= uint64(self.slice) << (64 - 41 - 13)
	id |= self.counter % 1024
	self.counter += 1
	return BitmapHandle(id)
}

func (self *Fragment) union(bitmaps []BitmapHandle) BitmapHandle {
	result := NewBitmap()
	for i, id := range bitmaps {
		bm, _ := self.getBitmap(id)
		if i == 0 {
			result = bm
		} else {
			result = Union(result, bm)
		}
	}
	return self.AllocHandle(result)
}
func (self *Fragment) intersect(bitmaps []BitmapHandle) BitmapHandle {
	var result IBitmap
	for i, id := range bitmaps {
		bm, _ := self.getBitmap(id)
		if i == 0 {
			result = Clone(bm)
		} else {
			result = Intersection(result, bm)
		}
	}
	return self.AllocHandle(result)
}

func (self *Fragment) ServeFragment() {
	for {
		req := <-self.requestChan
		self.mesg_count++
		start := time.Now()
		answer := req.Execute(self)
		delta := time.Since(start)
		self.mesg_count += 1
		self.mesg_time += delta
		/*
			var buffer bytes.Buffer
			buffer.WriteString(`{ "results":`)
			buffer.WriteString(answer)
			buffer.WriteString(fmt.Sprintf(`,"query type": "%s"`, responder.QueryType()))
			buffer.WriteString(fmt.Sprintf(`, "elapsed": "%s"}`, delta))
		*/
		req.ResponseChannel() <- Result{answer, delta}
	}
}

/*
type RequestJSON struct {
	Request  string
	Fragment string
	Args     json.RawMessage
}
func (a *FragmentContainer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	handler(w, r, a.fragments)
}

func (a *FragmentContainer) RunServer(porti int, closeChannel chan bool, started chan bool) {
	http.Handle("/", a)
	port := fmt.Sprintf(":%d", porti)

	s := &http.Server{
		Addr:           port,
		Handler:        nil,
		ReadTimeout:    10 * time.Second,
		WriteTimeout:   10 * time.Second,
		MaxHeaderBytes: 1 << 20,
	}

	l, e := net.Listen("tcp", port)
	if e != nil {
		log.Panicf(e.Error())
	}
	go s.Serve(l)
	started <- true
	select {
	case <-closeChannel:
		log.Printf("Server thread exit")
		l.Close()
		// Shutdown()
		return
		break
	}
}

func handler(w http.ResponseWriter, r *http.Request, fragments map[string]*Fragment) {
	if r.Method == "POST" {
		var f RequestJSON

		bin, _ := ioutil.ReadAll(r.Body)
		err := json.Unmarshal(bin, &f)

		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, fmt.Sprintf(`{ "error":"%s"}`, err))

		}
		decoder := json.NewDecoder(bytes.NewReader(f.Args))
		request := BuildCommandFactory(&f, decoder)
		w.Header().Set("Content-Type", "application/json")
		if request != nil {
			output := `{"Error":"Invalid Fragment"}`
			fc, found := fragments[f.Fragment] //f.FragmentIndex<len(fragments){
			if found {
				//   fc := fragments[f.FragmentGuid]
				fc.requestChan <- request
				output = request.GetResponder().Response()
			}
			fmt.Fprintf(w, output)
		} else {
			fmt.Fprintf(w, "NoOp")
		}
	}
}
*/